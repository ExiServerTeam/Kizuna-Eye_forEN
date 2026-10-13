package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func newTestConfigHandler(t *testing.T) *ConfigHandler {
	t.Helper()
	dir := t.TempDir()
	return NewConfigHandler(
		dir+"/agent.json",
		dir+"/dashboard.json",
		dir+"/modules.json",
	)
}

func TestValidateConfigRejectsMissingRequired(t *testing.T) {
	c := newTestConfigHandler(t)

	cases := []struct {
		typ string
		cfg interface{}
	}{
		{"agent", map[string]interface{}{"dashboard_url": ""}},
		{"dashboard", map[string]interface{}{"listen_addr": ""}},
		{"modules", []interface{}{map[string]interface{}{"name": ""}}},
		{"modules", map[string]interface{}{}}, // not an array
		{"modules", []interface{}{"not-an-object"}},
	}
	for _, cse := range cases {
		if err := c.validateConfig(cse.typ, cse.cfg); err == nil {
			t.Errorf("validateConfig(%q, %#v) = nil, want error", cse.typ, cse.cfg)
		}
	}
}

func TestValidateConfigAcceptsValid(t *testing.T) {
	c := newTestConfigHandler(t)
	if err := c.validateConfig("agent", map[string]interface{}{"dashboard_url": "ws://localhost:8080/ws"}); err != nil {
		t.Errorf("valid agent config rejected: %v", err)
	}
	if err := c.validateConfig("modules", []interface{}{map[string]interface{}{"name": "p1"}}); err != nil {
		t.Errorf("valid modules config rejected: %v", err)
	}
}

// An enabled Discord channel with no webhook_url must be rejected at save
// time; otherwise the server would fail to start on the next restart.
func TestValidateConfigRejectsEmptyDiscordWebhook(t *testing.T) {
	c := newTestConfigHandler(t)
	cfg := map[string]interface{}{
		"listen_addr": ":8080",
		"notifications": map[string]interface{}{
			"enabled": true,
			"channels": []interface{}{
				map[string]interface{}{"type": "discord", "enabled": true, "webhook_url": ""},
			},
		},
	}
	if err := c.validateConfig("dashboard", cfg); err == nil {
		t.Fatal("dashboard config with empty discord webhook_url should be rejected")
	}
}

func TestValidateConfigRejectsNonPositiveInterval(t *testing.T) {
	c := newTestConfigHandler(t)
	cfg := map[string]interface{}{"dashboard_url": "ws://localhost:8080/ws", "interval": float64(0)}
	if err := c.validateConfig("agent", cfg); err == nil {
		t.Fatal("agent config with interval <= 0 should be rejected")
	}
}

func TestValidateConfigAcceptsDisabledChannel(t *testing.T) {
	c := newTestConfigHandler(t)
	cfg := map[string]interface{}{
		"listen_addr": ":8080",
		"notifications": map[string]interface{}{
			"enabled": true,
			"channels": []interface{}{
				map[string]interface{}{"type": "discord", "enabled": false, "webhook_url": ""},
			},
		},
	}
	if err := c.validateConfig("dashboard", cfg); err != nil {
		t.Errorf("disabled channel should be allowed: %v", err)
	}
}

// The bulk endpoint must validate too, not just the per-type endpoint.
func TestBulkUpdateValidates(t *testing.T) {
	c := newTestConfigHandler(t)
	body := `{"agent":{"dashboard_url":""}}`
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
	rec := httptest.NewRecorder()

	c.handleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bulk update with invalid agent config: got status %d, want 400", rec.Code)
	}
}

// The bulk endpoint must still accept a valid config and persist it.
func TestBulkUpdateValidPersists(t *testing.T) {
	dir := t.TempDir()
	c := NewConfigHandler(dir+"/agent.json", dir+"/dashboard.json", dir+"/modules.json")

	body := `{"agent":{"dashboard_url":"ws://localhost:8080/ws"}}`
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
	rec := httptest.NewRecorder()
	c.handleUpdateConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid bulk update: got status %d, want 200", rec.Code)
	}
	data, err := os.ReadFile(c.agentConfigPath)
	if err != nil {
		t.Fatalf("read saved agent config: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	if got["dashboard_url"] != "ws://localhost:8080/ws" {
		t.Errorf("dashboard_url not persisted: %v", got["dashboard_url"])
	}
}
