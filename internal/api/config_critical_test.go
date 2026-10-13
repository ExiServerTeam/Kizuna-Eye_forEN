package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: a PUT that omits a security-critical top-level key must not
// silently delete it. The plugins signature policy, plugin-upload switch and
// alert-history signing key are all security boundaries: dropping any of them
// would silently weaken the deployment (unsigned .so accepted, upload RCE,
// history signatures disabled).
func TestUpdateDashboardConfigPreservesCriticalBlocks(t *testing.T) {
	dir := t.TempDir()
	dashPath := filepath.Join(dir, "dashboard_config.json")
	agentPath := filepath.Join(dir, "agent_config.json")
	modulesPath := filepath.Join(dir, "modules.json")

	existing := `{
  "listen_addr": ":8080",
  "plugins_upload_enabled": true,
  "alert_history_key_path": "/etc/kizuna-eye/alert_history.key",
  "plugins": {"require_signature": true, "public_key_file": "/etc/kizuna-eye/plugin_signing.pub"},
  "auth": {"enabled": true},
  "notifications": {"enabled": false}
}`
	if err := os.WriteFile(dashPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewConfigHandler(agentPath, dashPath, modulesPath)

	// A minimal body that omits every critical block (a scripted client that
	// "just changes the port").
	body := `{"listen_addr": ":9090"}`
	req := httptest.NewRequest(http.MethodPut, "/api/config/dashboard", strings.NewReader(body))
	req.SetPathValue("type", "dashboard")
	rec := httptest.NewRecorder()
	h.handleUpdateConfigByType(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	data, err := os.ReadFile(dashPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		`"require_signature": true`,
		`"plugins_upload_enabled": true`,
		`"alert_history_key_path": "/etc/kizuna-eye/alert_history.key"`,
		`"enabled": true`,        // auth
		`"listen_addr": ":9090"`, // the actual change must apply
	} {
		if !strings.Contains(s, want) {
			t.Errorf("critical value lost: %s not found in %s", want, s)
		}
	}
}
