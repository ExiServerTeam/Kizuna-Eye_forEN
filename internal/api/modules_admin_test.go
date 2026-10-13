package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newModuleWithPluginPath builds a storage with one module whose config has the
// given plugin_path, plus a handler that has no admin checker injected yet.
func newModuleWithPluginPath(t *testing.T, pluginPath string) (*ModulesStorage, *ModuleHandler) {
	t.Helper()
	dir := t.TempDir()
	s := NewModulesStorage(filepath.Join(dir, "modules.json"))
	cfg, err := json.Marshal(map[string]string{
		"plugin_path": pluginPath,
		"watch_files": "/var/log/auth.log",
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := s.Add(ModuleConfig{Name: "sec", Type: "plugin", Enabled: true, Config: cfg}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return s, NewModuleHandler(s, "")
}

func updateReq(name, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/api/modules/"+name, strings.NewReader(body))
	req.SetPathValue("name", name)
	return req
}

// operator (adminChecker=false) changing plugin_path must be refused, and the
// stored path must not change.
func TestHandleUpdatePluginPathRequiresAdmin(t *testing.T) {
	s, h := newModuleWithPluginPath(t, "/old/a.so")
	h.SetAdminChecker(func(*http.Request) bool { return false }) // operator

	rec := httptest.NewRecorder()
	h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","enabled":true,"config":{"plugin_path":"/new/b.so"}}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator plugin_path change = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := pluginPathOf(s.Get("sec").Config); got != "/old/a.so" {
		t.Fatalf("plugin_path changed despite 403: %q", got)
	}
}

// operator changing a non-protection setting must succeed. poll_interval_sec
// is operator-editable; the admin-only fields keep their stored values.
func TestHandleUpdateNonPathSettingsAllowedForOperator(t *testing.T) {
	_, h := newModuleWithPluginPath(t, "/a/x.so")
	h.SetAdminChecker(func(*http.Request) bool { return false }) // operator

	rec := httptest.NewRecorder()
	// Keep every admin-only field identical and enabled unchanged; only
	// poll_interval_sec changes.
	h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","enabled":true,"config":{"plugin_path":"/a/x.so","watch_files":"/var/log/auth.log","poll_interval_sec":30}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator settings edit = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// admin changing plugin_path must succeed and persist.
func TestHandleUpdatePluginPathAllowedForAdmin(t *testing.T) {
	s, h := newModuleWithPluginPath(t, "/old/a.so")
	h.SetAdminChecker(func(*http.Request) bool { return true }) // admin

	rec := httptest.NewRecorder()
	h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","enabled":true,"config":{"plugin_path":"/new/b.so"}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin plugin_path change = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := pluginPathOf(s.Get("sec").Config); got != "/new/b.so" {
		t.Fatalf("plugin_path not updated by admin: %q", got)
	}
}

// With no admin checker injected, a plugin_path change must be refused
// (fail-closed) rather than silently allowed.
func TestHandleUpdatePluginPathFailClosedWithoutChecker(t *testing.T) {
	_, h := newModuleWithPluginPath(t, "/old/a.so")
	// no SetAdminChecker call

	rec := httptest.NewRecorder()
	h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","enabled":true,"config":{"plugin_path":"/new/b.so"}}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("plugin_path change without checker = %d, want 403 (fail-closed)", rec.Code)
	}
}
