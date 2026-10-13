package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: saving the dashboard config from the editor with an existing
// Discord webhook left masked ("***") must succeed. The mask is restored from
// disk before validation, so the URL check sees the real URL. Validating
// first rejected the save with
// "notifications.channels[0]: discord の webhook_url が不正です: webhook_url は
// http または https のみ許可されます:".
func TestUpdateDashboardConfigMaskedWebhookValidatesAfterRestore(t *testing.T) {
	dir := t.TempDir()
	dashPath := filepath.Join(dir, "dashboard_config.json")
	agentPath := filepath.Join(dir, "agent_config.json")
	modulesPath := filepath.Join(dir, "modules.json")

	// Existing config with a real (public) Discord webhook.
	existing := `{
  "listen_addr": ":8080",
  "auth": {"enabled": true},
  "notifications": {
    "enabled": true,
    "channels": [
      {"type": "discord", "enabled": true, "webhook_url": "https://discord.com/api/webhooks/123/abc"}
    ]
  }
}`
	if err := os.WriteFile(dashPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewConfigHandler(agentPath, dashPath, modulesPath)

	// The editor sends the masked value back (as it received it), plus a
	// harmless change to prove the save is not a no-op.
	body := `{
  "listen_addr": ":8080",
  "log_level": "info",
  "auth": {"enabled": true},
  "notifications": {
    "enabled": true,
    "channels": [
      {"type": "discord", "enabled": true, "webhook_url": "***"}
    ]
  }
}`
	req := httptest.NewRequest(http.MethodPut, "/api/config/dashboard", strings.NewReader(body))
	req.SetPathValue("type", "dashboard")
	rec := httptest.NewRecorder()
	h.handleUpdateConfigByType(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("masked webhook save = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	// The real secret must have been preserved (not overwritten with "***").
	data, err := os.ReadFile(dashPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "https://discord.com/api/webhooks/123/abc") {
		t.Fatalf("real webhook_url was lost: %s", data)
	}
	if strings.Contains(string(data), "\"***\"") {
		t.Fatalf("mask placeholder was persisted: %s", data)
	}
}
