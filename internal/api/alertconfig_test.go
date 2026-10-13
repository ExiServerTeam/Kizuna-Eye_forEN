package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"Kizuna-Eye/pkg/alert"
	"Kizuna-Eye/pkg/config"
)

type stubAlertEngine struct{ cfg alert.Config }

func (s *stubAlertEngine) Snapshot() alert.Config      { return s.cfg }
func (s *stubAlertEngine) UpdateConfig(c alert.Config) { s.cfg = c }

func TestAlertConfigGetPut(t *testing.T) {
	eng := &stubAlertEngine{cfg: alert.Config{MemoryWarn: 80, MemoryCritical: 90}}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "dashboard_config.json")
	cfg := &config.DashboardConfig{}
	h := NewAlertConfigHandler(eng, cfg, cfgPath, nil)

	// GET
	rr := httptest.NewRecorder()
	h.handleGet(rr, httptest.NewRequest(http.MethodGet, "/api/alert-config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d", rr.Code)
	}

	// PUT
	body, _ := json.Marshal(alertConfigPayload{
		MemoryWarnPct: 70, MemoryCriticalPct: 85,
		DiskFreeWarnPct: 20, DiskFreeCriticalPct: 10,
		CPUTempWarnC: 70, CPUTempCriticalC: 85, NotifyRecovery: true,
	})
	rr = httptest.NewRecorder()
	h.handlePut(rr, httptest.NewRequest(http.MethodPut, "/api/alert-config", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("put status = %d (%s)", rr.Code, rr.Body.String())
	}
	if eng.cfg.MemoryWarn != 70 {
		t.Errorf("engine not updated: %v", eng.cfg.MemoryWarn)
	}
	// Verify persistence file was written.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not persisted: %v", err)
	}
	if !bytes.Contains(data, []byte("70")) {
		t.Errorf("persisted config does not contain new value: %s", data)
	}
}

func TestAlertConfigRejectsOutOfRange(t *testing.T) {
	eng := &stubAlertEngine{}
	h := NewAlertConfigHandler(eng, nil, "", nil)
	body, _ := json.Marshal(alertConfigPayload{MemoryWarnPct: 150})
	rr := httptest.NewRecorder()
	h.handlePut(rr, httptest.NewRequest(http.MethodPut, "/api/alert-config", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// 負のCPU温度は拒否される。
func TestAlertConfigRejectsNegativeCPUTemp(t *testing.T) {
	eng := &stubAlertEngine{}
	h := NewAlertConfigHandler(eng, nil, "", nil)
	body, _ := json.Marshal(alertConfigPayload{
		MemoryWarnPct: 70, MemoryCriticalPct: 85,
		DiskFreeWarnPct: 20, DiskFreeCriticalPct: 10,
		CPUTempWarnC: -5, CPUTempCriticalC: 85,
	})
	rr := httptest.NewRecorder()
	h.handlePut(rr, httptest.NewRequest(http.MethodPut, "/api/alert-config", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("negative cpu temp: status = %d, want 400", rr.Code)
	}
}

// When the config file cannot be saved, the running engine and the in-memory
// config must NOT change (no silent loss on restart).
func TestAlertConfigRollsBackWhenPersistFails(t *testing.T) {
	eng := &stubAlertEngine{cfg: alert.Config{MemoryWarn: 80, MemoryCritical: 90}}

	// Make the config path unsavable: its parent is a regular file.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(blocker, "dashboard_config.json")

	cfg := &config.DashboardConfig{}
	cfg.Notifications.MemoryWarnPct = 80
	cfg.Notifications.MemoryCriticalPct = 90
	h := NewAlertConfigHandler(eng, cfg, cfgPath, nil)

	body, _ := json.Marshal(alertConfigPayload{
		MemoryWarnPct: 70, MemoryCriticalPct: 85,
		DiskFreeWarnPct: 20, DiskFreeCriticalPct: 10,
		CPUTempWarnC: 70, CPUTempCriticalC: 85, NotifyRecovery: true,
	})
	rr := httptest.NewRecorder()
	h.handlePut(rr, httptest.NewRequest(http.MethodPut, "/api/alert-config", bytes.NewReader(body)))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (save should fail)", rr.Code)
	}
	if eng.cfg.MemoryWarn != 80 {
		t.Errorf("engine must not change on failed persist: got %v", eng.cfg.MemoryWarn)
	}
	if cfg.Notifications.MemoryWarnPct != 80 {
		t.Errorf("in-memory cfg must be rolled back: got %v", cfg.Notifications.MemoryWarnPct)
	}
}

// 警告閾値が危険閾値より高い設定は拒否される。
func TestAlertConfigRejectsWarnAboveCritical(t *testing.T) {
	cases := []alertConfigPayload{
		{MemoryWarnPct: 90, MemoryCriticalPct: 85, DiskFreeWarnPct: 20, DiskFreeCriticalPct: 10, CPUTempWarnC: 70, CPUTempCriticalC: 85},
		{MemoryWarnPct: 70, MemoryCriticalPct: 85, DiskFreeWarnPct: 10, DiskFreeCriticalPct: 20, CPUTempWarnC: 70, CPUTempCriticalC: 85},
		{MemoryWarnPct: 70, MemoryCriticalPct: 85, DiskFreeWarnPct: 20, DiskFreeCriticalPct: 10, CPUTempWarnC: 90, CPUTempCriticalC: 85},
	}
	for i, p := range cases {
		eng := &stubAlertEngine{}
		h := NewAlertConfigHandler(eng, nil, "", nil)
		body, _ := json.Marshal(p)
		rr := httptest.NewRecorder()
		h.handlePut(rr, httptest.NewRequest(http.MethodPut, "/api/alert-config", bytes.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("case %d: status = %d, want 400", i, rr.Code)
		}
	}
}
