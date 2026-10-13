package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"Kizuna-Eye/pkg/alert"
	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/fsutil"
	kelog "Kizuna-Eye/pkg/logger"
)

// AlertConfigProvider is implemented by the alert engine.
type AlertConfigProvider interface {
	Snapshot() alert.Config
	UpdateConfig(alert.Config)
}

// AlertConfigLogger is a minimal logger.
type AlertConfigLogger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
}

// AlertConfigHandler serves runtime alert threshold settings.
type AlertConfigHandler struct {
	engine     AlertConfigProvider
	cfg        *config.DashboardConfig
	configPath string
	logger     AlertConfigLogger

	// mu serializes writes to cfg. handlePut mutates cfg.Notifications and
	// then marshals the whole cfg, so two concurrent PUTs would race on that
	// shared struct (and could persist a half-updated config).
	mu sync.Mutex
}

// NewAlertConfigHandler creates an AlertConfigHandler.
func NewAlertConfigHandler(engine AlertConfigProvider, cfg *config.DashboardConfig, configPath string, logger AlertConfigLogger) *AlertConfigHandler {
	return &AlertConfigHandler{engine: engine, cfg: cfg, configPath: configPath, logger: logger}
}

// RegisterRoutes registers the alert config routes.
func (h *AlertConfigHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/alert-config", h.handleGet)
	mux.HandleFunc("PUT /api/alert-config", h.handlePut)
}

// payload is the JSON shape for the API.
type alertConfigPayload struct {
	MemoryWarnPct       float64 `json:"memory_warn_pct"`
	MemoryCriticalPct   float64 `json:"memory_critical_pct"`
	DiskFreeWarnPct     float64 `json:"disk_free_warn_pct"`
	DiskFreeCriticalPct float64 `json:"disk_free_critical_pct"`
	CPUTempWarnC        float64 `json:"cpu_temp_warn_c"`
	CPUTempCriticalC    float64 `json:"cpu_temp_critical_c"`
	NotifyRecovery      bool    `json:"notify_recovery"`
}

func (h *AlertConfigHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	c := h.engine.Snapshot()
	writeJSON(w, http.StatusOK, alertConfigPayload{
		MemoryWarnPct:       c.MemoryWarn,
		MemoryCriticalPct:   c.MemoryCritical,
		DiskFreeWarnPct:     c.DiskFreeWarn,
		DiskFreeCriticalPct: c.DiskFreeCritical,
		CPUTempWarnC:        c.CPUTempWarn,
		CPUTempCriticalC:    c.CPUTempCritical,
		NotifyRecovery:      c.NotifyRecovery,
	})
}

func (h *AlertConfigHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	var p alertConfigPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// Range validation.
	if err := validatePct("memory_warn_pct", p.MemoryWarnPct); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePct("memory_critical_pct", p.MemoryCriticalPct); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePct("disk_free_warn_pct", p.DiskFreeWarnPct); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePct("disk_free_critical_pct", p.DiskFreeCriticalPct); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// CPU温度は 0〜150℃ の範囲に限定する。負値や異常値を弾く。
	if err := validateTemp("cpu_temp_warn_c", p.CPUTempWarnC); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateTemp("cpu_temp_critical_c", p.CPUTempCriticalC); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 警告閾値が危険閾値より高いのは設定ミス。
	if p.MemoryWarnPct > p.MemoryCriticalPct {
		writeJSONError(w, http.StatusBadRequest, "memory_warn_pct は memory_critical_pct 以下にしてください")
		return
	}
	if p.DiskFreeCriticalPct > p.DiskFreeWarnPct {
		writeJSONError(w, http.StatusBadRequest, "disk_free_critical_pct は disk_free_warn_pct 以下にしてください")
		return
	}
	if p.CPUTempWarnC > p.CPUTempCriticalC {
		writeJSONError(w, http.StatusBadRequest, "cpu_temp_warn_c は cpu_temp_critical_c 以下にしてください")
		return
	}

	cur := h.engine.Snapshot()
	cur.MemoryWarn = p.MemoryWarnPct
	cur.MemoryCritical = p.MemoryCriticalPct
	cur.DiskFreeWarn = p.DiskFreeWarnPct
	cur.DiskFreeCritical = p.DiskFreeCriticalPct
	cur.CPUTempWarn = p.CPUTempWarnC
	cur.CPUTempCritical = p.CPUTempCriticalC
	cur.NotifyRecovery = p.NotifyRecovery

	// Serialize cfg mutation + persist so concurrent PUTs cannot race on
	// h.cfg (mutate-while-marshal) or interleave their rollbacks.
	h.mu.Lock()
	defer h.mu.Unlock()

	// Persist to dashboard_config.json BEFORE applying the change in memory,
	// so a failed save cannot leave the running state (engine + cfg) out of
	// sync with the file and silently lost on restart.
	if h.cfg != nil {
		prev := h.cfg.Notifications
		h.cfg.Notifications.MemoryWarnPct = p.MemoryWarnPct
		h.cfg.Notifications.MemoryCriticalPct = p.MemoryCriticalPct
		h.cfg.Notifications.DiskFreeWarnPct = p.DiskFreeWarnPct
		h.cfg.Notifications.DiskFreeCriticalPct = p.DiskFreeCriticalPct
		h.cfg.Notifications.CPUTempWarnC = p.CPUTempWarnC
		h.cfg.Notifications.CPUTempCriticalC = p.CPUTempCriticalC
		h.cfg.Notifications.NotifyRecovery = p.NotifyRecovery

		if err := h.persistConfig(); err != nil {
			// Roll back the in-memory config and do not touch the engine, so
			// nothing changes when the save fails.
			h.cfg.Notifications = prev
			writeJSONError(w, http.StatusInternalServerError, "設定の保存に失敗しました: "+err.Error())
			return
		}
		if h.logger != nil {
			kelog.LogInfo(h.logger, "api.alertcfg_updated")
		}
	}

	// Only apply to the running engine after a successful persist.
	h.engine.UpdateConfig(cur)

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// persistConfig writes the current config back to dashboard_config.json.
// It writes atomically (temp + rename) and preserves the existing file mode,
// so a tightened permission (e.g. 0600 for a webhook-bearing config) is not
// silently widened to 0644 on every threshold save.
func (h *AlertConfigHandler) persistConfig() error {
	if h.configPath == "" {
		return fmt.Errorf("config path is empty")
	}
	data, err := json.MarshalIndent(h.cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(h.configPath)
	// Config files hold secrets; keep the directory owner-only too.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if info, statErr := os.Stat(h.configPath); statErr == nil {
		mode = info.Mode().Perm()
	}

	// fsutil.WriteFileAtomic: シンボリックリンクを拒否し、mode を適用してから
	// fsync + rename する（L-12: 同じ処理が12箇所に重複していた）。
	return fsutil.WriteFileAtomic(h.configPath, data, mode)
}

func validatePct(name string, v float64) error {
	if v < 0 || v > 100 {
		return fmt.Errorf("%s は 0〜100 の範囲で指定してください", name)
	}
	return nil
}

// validateTemp validates a CPU temperature threshold (0〜150℃).
func validateTemp(name string, v float64) error {
	if v < 0 || v > 150 {
		return fmt.Errorf("%s は 0〜150 の範囲で指定してください", name)
	}
	return nil
}
