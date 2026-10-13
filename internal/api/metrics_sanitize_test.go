package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"Kizuna-Eye/pkg/status"
)

// stubHub serves a fixed status.
type stubHub struct{ s *status.SystemStatus }

func (h stubHub) GetLastStatus() *status.SystemStatus { return h.s }

// TestMetricsViewerSanitization pins the fix: when the sanitizer predicate says
// the request is an unauthenticated public viewer, the metrics endpoint must
// omit the process count and the per-disk S.M.A.R.T values, matching what
// /api/status already hides.
func TestMetricsViewerSanitization(t *testing.T) {
	s := &status.SystemStatus{
		Timestamp:  1,
		CPUUsage:   10,
		MemPercent: 20,
		Disks: []status.DiskInfo{
			{Path: "/", Percent: 30, Health: "FAILED", Temp: 55},
		},
		Processes: []status.ProcessInfo{{PID: 1, Name: "systemd", CPU: 1, MemMB: 5}},
	}

	collect := func(sanitize bool) string {
		h := NewMetricHandler(stubHub{s: s})
		if sanitize {
			h.SetSanitizer(func(*http.Request) bool { return true })
		}
		req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
		rec := httptest.NewRecorder()
		h.handlePrometheus(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		return rec.Body.String()
	}

	authed := collect(false)
	if !strings.Contains(authed, "kizuna_process_count") {
		t.Fatal("authenticated view should include the process count")
	}
	if !strings.Contains(authed, "kizuna_disk_health_status") {
		t.Fatal("authenticated view should include disk health")
	}

	viewer := collect(true)
	if strings.Contains(viewer, "kizuna_process_count") {
		t.Fatal("viewer view must not include the process count")
	}
	if strings.Contains(viewer, "kizuna_disk_health_status") {
		t.Fatal("viewer view must not include disk S.M.A.R.T health")
	}
	if !strings.Contains(viewer, "kizuna_memory_usage_percent") {
		t.Fatal("viewer view must still include usage metrics")
	}
}
