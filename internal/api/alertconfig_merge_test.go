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

// TestAlertConfigSaveDoesNotRevertOtherKeys pins the data-loss fix: saving a
// threshold used to marshal the WHOLE in-memory config, so any other key that
// changed on disk after boot (webhook, SMTP password, listen_addr, plugin
// signature policy) was silently rolled back to the startup snapshot.
func TestAlertConfigSaveDoesNotRevertOtherKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "dashboard_config.json")

	// In-memory snapshot at boot: one webhook, notifications enabled.
	cfg := &config.DashboardConfig{}
	cfg.ListenAddr = ":8080"
	cfg.Notifications.Enabled = true
	cfg.Notifications.MemoryWarnPct = 80
	cfg.Notifications.MemoryCriticalPct = 90

	// On disk the operator has since changed the webhook and added auth.
	onDisk := `{
  "listen_addr": ":9999",
  "auth": {"enabled": true},
  "notifications": {
    "enabled": true,
    "memory_warn_pct": 80,
    "memory_critical_pct": 90,
    "channels": [{"type": "discord", "enabled": true, "webhook_url": "https://discord.com/api/webhooks/1/abc"}]
  }
}`
	if err := os.WriteFile(cfgPath, []byte(onDisk), 0o600); err != nil {
		t.Fatal(err)
	}

	eng := &stubAlertEngine{cfg: alert.Config{MemoryWarn: 80, MemoryCritical: 90}}
	h := NewAlertConfigHandler(eng, cfg, cfgPath, nil)

	body, _ := json.Marshal(alertConfigPayload{
		MemoryWarnPct: 70, MemoryCriticalPct: 85,
		DiskFreeWarnPct: 20, DiskFreeCriticalPct: 10,
		CPUTempWarnC: 70, CPUTempCriticalC: 85, NotifyRecovery: true,
	})
	rr := httptest.NewRecorder()
	h.handlePut(rr, httptest.NewRequest(http.MethodPut, "/api/alert-config", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// The threshold must be updated...
	if !bytes.Contains(got, []byte(`"memory_warn_pct": 70`)) {
		t.Errorf("threshold not persisted: %s", got)
	}
	// ...and the keys edited after boot must NOT be reverted.
	if !bytes.Contains(got, []byte(`"listen_addr": ":9999"`)) {
		t.Errorf("listen_addr was reverted to the startup snapshot: %s", got)
	}
	if !bytes.Contains(got, []byte(`"auth"`)) {
		t.Errorf("auth block was dropped by the save: %s", got)
	}
	if !bytes.Contains(got, []byte(`discord.com/api/webhooks/1/abc`)) {
		t.Errorf("webhook was reverted or dropped: %s", got)
	}
}

// A file that is not valid JSON must be refused, not clobbered.
func TestAlertConfigSaveRefusesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "dashboard_config.json")
	if err := os.WriteFile(cfgPath, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.DashboardConfig{}
	eng := &stubAlertEngine{}
	h := NewAlertConfigHandler(eng, cfg, cfgPath, nil)

	body, _ := json.Marshal(alertConfigPayload{
		MemoryWarnPct: 70, MemoryCriticalPct: 85,
		DiskFreeWarnPct: 20, DiskFreeCriticalPct: 10,
		CPUTempWarnC: 70, CPUTempCriticalC: 85,
	})
	rr := httptest.NewRecorder()
	h.handlePut(rr, httptest.NewRequest(http.MethodPut, "/api/alert-config", bytes.NewReader(body)))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a corrupt config", rr.Code)
	}
	got, _ := os.ReadFile(cfgPath)
	if !bytes.Contains(got, []byte("not json")) {
		t.Errorf("corrupt file must be left untouched, got: %s", got)
	}
}
