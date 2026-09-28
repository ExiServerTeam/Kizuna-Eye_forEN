package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"Kizuna-Eye/pkg/status"
)

type stubStatusHub struct{ s *status.SystemStatus }

func (h stubStatusHub) GetLastStatus() *status.SystemStatus { return h.s }

// Regression: CPU metric is emitted under the correct key (cpu_usage).
func TestMetricsIncludesCPUUsage(t *testing.T) {
	h := NewMetricHandler(stubStatusHub{s: &status.SystemStatus{
		CPUUsage:   42.5,
		MemPercent: 50,
		MemUsed:    1024,
		Uptime:     3600,
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	rr := httptest.NewRecorder()
	h.handlePrometheus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "kizuna_cpu_usage_percent 42.5") {
		t.Errorf("cpu_usage metric missing or wrong:\n%s", body)
	}
	if !strings.Contains(body, "kizuna_up 1") {
		t.Errorf("kizuna_up missing:\n%s", body)
	}
	if !strings.Contains(body, "kizuna_uptime_seconds 3600") {
		t.Errorf("uptime metric missing:\n%s", body)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

// Returns 404 (no panic) when status is nil.
func TestMetricsNilStatus(t *testing.T) {
	h := NewMetricHandler(stubStatusHub{s: nil})
	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	rr := httptest.NewRecorder()
	h.handlePrometheus(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("Content-Type should be text/plain even on error")
	}
}

func TestExampleFileFor(t *testing.T) {
	cases := map[string]string{
		"agent":     "agent_config.example.json",
		"dashboard": "dashboard_config.example.json",
		"modules":   "modules.json.example",
	}
	for in, want := range cases {
		got, ok := exampleFileFor(in)
		if !ok || got != want {
			t.Errorf("exampleFileFor(%q) = (%q,%v), want (%q,true)", in, got, ok, want)
		}
	}
	if _, ok := exampleFileFor("unknown"); ok {
		t.Error("exampleFileFor(unknown) should return false")
	}
}
