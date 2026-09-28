package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"Kizuna-Eye/pkg/status"
)

// StatusHub provides the latest system status.
type StatusHub interface {
	GetLastStatus() *status.SystemStatus
}

// MetricHandler serves Prometheus metrics.
type MetricHandler struct {
	hub StatusHub
}

// NewMetricHandler creates a MetricHandler.
func NewMetricHandler(hub StatusHub) *MetricHandler {
	return &MetricHandler{hub: hub}
}

// RegisterRoutes registers the metrics route.
func (m *MetricHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/metrics", m.handlePrometheus)
}

// handlePrometheus writes metrics in Prometheus text format.
func (m *MetricHandler) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	// Always return text/plain, even on error.
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	s := m.hub.GetLastStatus()
	if s == nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintln(w, "# No status available")
		return
	}

	ts := time.Now().Unix()
	var b strings.Builder

	// Liveness
	gauge(&b, "kizuna_up", "1 if the last status is available", 1, ts)

	// Status timestamp and age
	gaugeInt(&b, "kizuna_status_timestamp_seconds", "Unix timestamp of the last collected status", float64(s.Timestamp), ts)
	if s.Timestamp > 0 {
		gaugeInt(&b, "kizuna_status_age_seconds", "Seconds since the last collected status", float64(ts-s.Timestamp), ts)
	}

	// Memory
	gaugeInt(&b, "kizuna_memory_used_bytes", "Memory usage in bytes", float64(s.MemUsed), ts)
	gaugeInt(&b, "kizuna_memory_total_bytes", "Total memory in bytes", float64(s.MemTotal), ts)
	gaugeInt(&b, "kizuna_memory_free_bytes", "Free memory in bytes", float64(freeBytes(s.MemTotal, s.MemUsed)), ts)
	gauge(&b, "kizuna_memory_usage_percent", "Memory usage percentage", s.MemPercent, ts)

	// CPU
	gauge(&b, "kizuna_cpu_usage_percent", "CPU usage percentage", s.CPUUsage, ts)
	if len(s.CPUPerCore) > 0 {
		fmt.Fprintf(&b, "# HELP kizuna_cpu_core_usage_percent Per-core CPU usage percentage\n")
		fmt.Fprintf(&b, "# TYPE kizuna_cpu_core_usage_percent gauge\n")
		for i, v := range s.CPUPerCore {
			fmt.Fprintf(&b, "kizuna_cpu_core_usage_percent{core=\"%d\"} %g %d\n", i, v, ts)
		}
	}

	// Load average
	if len(s.LoadAverage) >= 3 {
		gauge(&b, "kizuna_load_average_1m", "1-minute load average", s.LoadAverage[0], ts)
		gauge(&b, "kizuna_load_average_5m", "5-minute load average", s.LoadAverage[1], ts)
		gauge(&b, "kizuna_load_average_15m", "15-minute load average", s.LoadAverage[2], ts)
	}

	// Disk (totals)
	gaugeInt(&b, "kizuna_disk_total_bytes", "Total disk space in bytes", float64(s.DiskTotal), ts)
	gaugeInt(&b, "kizuna_disk_used_bytes", "Used disk space in bytes", float64(s.DiskUsed), ts)
	gaugeInt(&b, "kizuna_disk_free_bytes", "Free disk space in bytes", float64(freeBytes(s.DiskTotal, s.DiskUsed)), ts)
	gauge(&b, "kizuna_disk_usage_percent", "Disk usage percentage", s.DiskPercent, ts)

	// Per-disk metrics
	if len(s.Disks) > 0 {
		fmt.Fprintf(&b, "# HELP kizuna_disk_mount_usage_percent Per-mount disk usage percentage\n")
		fmt.Fprintf(&b, "# TYPE kizuna_disk_mount_usage_percent gauge\n")
		for _, d := range s.Disks {
			path := escapeLabel(d.Path)
			fmt.Fprintf(&b, "kizuna_disk_mount_usage_percent{mount=\"%s\"} %g %d\n", path, d.Percent, ts)
		}
		fmt.Fprintf(&b, "# HELP kizuna_disk_mount_free_bytes Per-mount free disk space in bytes\n")
		fmt.Fprintf(&b, "# TYPE kizuna_disk_mount_free_bytes gauge\n")
		for _, d := range s.Disks {
			path := escapeLabel(d.Path)
			fmt.Fprintf(&b, "kizuna_disk_mount_free_bytes{mount=\"%s\"} %d %d\n", path, freeBytes(d.Total, d.Used), ts)
		}
		// Disk temperature (only when available, HELP/TYPE once)
		hasTemp := false
		for _, d := range s.Disks {
			if d.Temp > 0 {
				hasTemp = true
				break
			}
		}
		if hasTemp {
			fmt.Fprintf(&b, "# HELP kizuna_disk_temp_celsius Disk temperature in Celsius\n")
			fmt.Fprintf(&b, "# TYPE kizuna_disk_temp_celsius gauge\n")
			for _, d := range s.Disks {
				if d.Temp > 0 {
					fmt.Fprintf(&b, "kizuna_disk_temp_celsius{mount=\"%s\"} %g %d\n", escapeLabel(d.Path), d.Temp, ts)
				}
			}
		}
		fmt.Fprintf(&b, "# HELP kizuna_disk_health_status Disk S.M.A.R.T health (1=PASSED, 0=FAILED, -1=unknown)\n")
		fmt.Fprintf(&b, "# TYPE kizuna_disk_health_status gauge\n")
		for _, d := range s.Disks {
			fmt.Fprintf(&b, "kizuna_disk_health_status{mount=\"%s\"} %d %d\n", escapeLabel(d.Path), healthValue(d.Health), ts)
		}
	}

	// CPU temperature (only when > 0)
	if s.CPUTemp > 0 {
		gauge(&b, "kizuna_cpu_temp_celsius", "CPU temperature in Celsius", s.CPUTemp, ts)
	}

	// Uptime
	gaugeInt(&b, "kizuna_uptime_seconds", "System uptime in seconds", float64(s.Uptime), ts)

	// Process count
	if len(s.Processes) > 0 {
		gaugeInt(&b, "kizuna_process_count", "Number of processes reported (Top N)", float64(len(s.Processes)), ts)
	}

	// Error and fallback state
	gaugeInt(&b, "kizuna_cpu_error_count", "Consecutive CPU collection error count", float64(s.CPUErrorCount), ts)
	gauge(&b, "kizuna_fallback_active", "1 if the collector is running in fallback mode", boolToFloat(s.Fallback), ts)
	gauge(&b, "kizuna_collector_error", "1 if the last collection reported an error", boolToFloat(s.LastError != ""), ts)

	// Network counters (cumulative)
	if s.Network != nil {
		counterInt(&b, "kizuna_network_up_bytes", "Total bytes sent", float64(s.Network.Up), ts)
		counterInt(&b, "kizuna_network_down_bytes", "Total bytes received", float64(s.Network.Down), ts)
	}

	// Network speed
	if s.NetworkSpeed != nil {
		gaugeInt(&b, "kizuna_network_up_speed_bytes_per_second", "Network upload speed in bytes/s", float64(s.NetworkSpeed.Up), ts)
		gaugeInt(&b, "kizuna_network_down_speed_bytes_per_second", "Network download speed in bytes/s", float64(s.NetworkSpeed.Down), ts)
	}

	// Backup
	if s.Backup.Status != "" || s.Backup.LastRun != "" {
		gaugeInt(&b, "kizuna_backup_size_bytes", "Last backup size in bytes", float64(s.Backup.Size), ts)
		gauge(&b, "kizuna_backup_success", "1 if the last backup succeeded", boolToFloat(isBackupSuccess(s.Backup.Status)), ts)
	}

	w.Write([]byte(b.String()))
}

// gauge writes a gauge metric with a float value.
func gauge(b *strings.Builder, name, help string, val float64, ts int64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %g %d\n", name, help, name, name, val, ts)
}

// gaugeInt writes a gauge metric with an integer value.
func gaugeInt(b *strings.Builder, name, help string, val float64, ts int64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %d %d\n", name, help, name, name, int64(val), ts)
}

// counterInt writes a counter metric with an integer value.
func counterInt(b *strings.Builder, name, help string, val float64, ts int64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n%s %d %d\n", name, help, name, name, int64(val), ts)
}

// freeBytes returns total-used, clamped to 0.
func freeBytes(total, used uint64) uint64 {
	if total > used {
		return total - used
	}
	return 0
}

// boolToFloat converts a bool to 0 or 1.
func boolToFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// healthValue converts an S.M.A.R.T health string to a metric value.
func healthValue(health string) int {
	switch health {
	case "PASSED":
		return 1
	case "FAILED":
		return 0
	default:
		return -1
	}
}

// isBackupSuccess reports whether a backup status means success.
func isBackupSuccess(status string) bool {
	switch strings.ToLower(status) {
	case "success", "completed", "ok":
		return true
	default:
		return false
	}
}

// escapeLabel escapes a Prometheus label value.
func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}
