package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression for the enabled zero-value trap: a partial update that omits
// "enabled" (e.g. a config-only edit) must NOT silently disable the module.
// A plain bool decoded to false and looked like a change, so an admin's
// settings edit downgraded the module's protection.
func TestHandleUpdateOmitsEnabledKeepsStored(t *testing.T) {
	s, h := newModuleWithPluginPath(t, "/a/x.so") // added with Enabled: true
	h.SetAdminChecker(func(*http.Request) bool { return true })

	rec := httptest.NewRecorder()
	// No "enabled" field: only a (non-admin) setting change.
	h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","config":{"plugin_path":"/a/x.so","watch_files":"/var/log/auth.log","poll_interval_sec":30}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("omit-enabled update = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := s.Get("sec"); got == nil || !got.Enabled {
		t.Fatalf("omitting enabled disabled the module: %+v", got)
	}
}

// An explicit "enabled": false must still disable the module (admin).
func TestHandleUpdateExplicitDisableWorks(t *testing.T) {
	s, h := newModuleWithPluginPath(t, "/a/x.so")
	h.SetAdminChecker(func(*http.Request) bool { return true })

	rec := httptest.NewRecorder()
	h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","enabled":false,"config":{"plugin_path":"/a/x.so","watch_files":"/var/log/auth.log"}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("explicit disable = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := s.Get("sec"); got == nil || got.Enabled {
		t.Fatalf("explicit disable did not take effect: %+v", got)
	}
}
