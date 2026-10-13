package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUpdateModuleTypeRequiresAdmin pins the fix for a privilege gap: changing
// a module's Type from "plugin" makes the agent skip loading it, which stops
// its monitoring just like disabling it. That is a protection downgrade, so it
// must require admin even though the path-based rule allows operator PUTs.
func TestUpdateModuleTypeRequiresAdmin(t *testing.T) {
	dir := t.TempDir()
	store := NewModulesStorage(dir + "/modules.json")
	if err := store.Add(ModuleConfig{
		Name:    "kizuna_security",
		Type:    "plugin",
		Enabled: true,
		Config:  json.RawMessage(`{"plugin_path":"/opt/kizuna-eye/bin/plugins/kizuna_security.so"}`),
	}); err != nil {
		t.Fatalf("seed module: %v", err)
	}

	// Operator: type change is refused and the stored type is unchanged.
	h := NewModuleHandler(store, dir)
	h.SetAdminChecker(func(*http.Request) bool { return false })

	body := `{"name":"kizuna_security","type":"watcher","enabled":true,"config":{"plugin_path":"/opt/kizuna-eye/bin/plugins/kizuna_security.so"}}`
	req := httptest.NewRequest(http.MethodPut, "/api/modules/kizuna_security", strings.NewReader(body))
	req.SetPathValue("name", "kizuna_security")
	rec := httptest.NewRecorder()
	h.handleUpdate(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator type change: got status %d, want 403", rec.Code)
	}
	if got := store.Get("kizuna_security"); got == nil || got.Type != "plugin" {
		t.Fatalf("stored type changed despite the refusal: %+v", got)
	}

	// Admin: the same change is allowed.
	h2 := NewModuleHandler(store, dir)
	h2.SetAdminChecker(func(*http.Request) bool { return true })
	req2 := httptest.NewRequest(http.MethodPut, "/api/modules/kizuna_security", strings.NewReader(body))
	req2.SetPathValue("name", "kizuna_security")
	rec2 := httptest.NewRecorder()
	h2.handleUpdate(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("admin type change: got status %d, want 200 (body %s)", rec2.Code, rec2.Body.String())
	}
}
