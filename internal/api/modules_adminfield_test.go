package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// newModuleWithConfig builds a storage with one module carrying the given raw
// config JSON, plus a handler with no admin checker injected yet.
func newModuleWithConfig(t *testing.T, cfgJSON string) (*ModulesStorage, *ModuleHandler) {
	t.Helper()
	dir := t.TempDir()
	s := NewModulesStorage(filepath.Join(dir, "modules.json"))
	if !json.Valid([]byte(cfgJSON)) {
		t.Fatalf("test config is not valid JSON: %s", cfgJSON)
	}
	if err := s.Add(ModuleConfig{Name: "sec", Type: "plugin", Enabled: true, Config: json.RawMessage(cfgJSON)}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return s, NewModuleHandler(s, "")
}

// adminOnlyCases lists representative admin-only fields with two distinct
// values. It covers a protection path, a protection toggle, a baseline path, a
// detection target list, and the block mode.
var adminOnlyCases = []struct {
	field string
	old   string
	new   string
}{
	{"chain_key_path", `"/old/chain.key"`, `"/new/chain.key"`},
	{"integrity_baseline_path", `"/old/fim.json"`, `"/new/fim.json"`},
	{"integrity_files", `"/etc/passwd"`, `"/etc/hosts"`},
	{"watch_files", `"/var/log/auth.log"`, `"/tmp/x.log"`},
	{"block_mode", `"off"`, `"enforce"`},
	{"fim_watch", `false`, `true`},
	{"suid_check", `true`, `false`},
	{"listen_port_check", `true`, `false`},
}

// An operator (adminChecker=false) must not be able to change any admin-only
// field, and the stored value must stay unchanged.
func TestHandleUpdateAdminOnlyFieldsForbiddenForOperator(t *testing.T) {
	for _, tc := range adminOnlyCases {
		t.Run(tc.field, func(t *testing.T) {
			oldCfg := `{"` + tc.field + `":` + tc.old + `}`
			newCfg := `{"` + tc.field + `":` + tc.new + `}`
			s, h := newModuleWithConfig(t, oldCfg)
			h.SetAdminChecker(func(*http.Request) bool { return false }) // operator

			rec := httptest.NewRecorder()
			h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","enabled":true,"config":`+newCfg+`}`))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("operator %s change = %d, want 403 (body=%s)", tc.field, rec.Code, rec.Body.String())
			}
			// The stored value must not have changed.
			got := rawConfigMap(s.Get("sec").Config)[tc.field]
			want := rawConfigMap(json.RawMessage(oldCfg))[tc.field]
			if string(got) != string(want) {
				t.Fatalf("%s changed despite 403: got %s want %s", tc.field, got, want)
			}
		})
	}
}

// An admin (adminChecker=true) may change an admin-only field.
func TestHandleUpdateAdminOnlyFieldAllowedForAdmin(t *testing.T) {
	s, h := newModuleWithConfig(t, `{"chain_key_path":"/old/chain.key"}`)
	h.SetAdminChecker(func(*http.Request) bool { return true }) // admin

	rec := httptest.NewRecorder()
	h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","enabled":true,"config":{"chain_key_path":"/new/chain.key"}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin change = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := string(rawConfigMap(s.Get("sec").Config)["chain_key_path"]); got != `"/new/chain.key"` {
		t.Fatalf("chain_key_path not updated by admin: %s", got)
	}
}

// With no admin checker injected, changing an admin-only field must be refused
// (fail-closed).
func TestHandleUpdateAdminOnlyFieldFailClosedWithoutChecker(t *testing.T) {
	_, h := newModuleWithConfig(t, `{"suid_check":true}`)
	// no SetAdminChecker call

	rec := httptest.NewRecorder()
	h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","enabled":true,"config":{"suid_check":false}}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("change without checker = %d, want 403 (fail-closed)", rec.Code)
	}
}

// An operator may change a non-protection setting while keeping every
// admin-only field identical to its stored value.
func TestHandleUpdateOperatorAllowedFieldWithSameAdminFields(t *testing.T) {
	_, h := newModuleWithConfig(t, `{"chain_key_path":"/a/chain.key","suid_check":true,"poll_interval_sec":15}`)
	h.SetAdminChecker(func(*http.Request) bool { return false }) // operator

	rec := httptest.NewRecorder()
	h.handleUpdate(rec, updateReq("sec", `{"name":"sec","type":"plugin","enabled":true,"config":{"chain_key_path":"/a/chain.key","suid_check":true,"poll_interval_sec":60}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("operator allowed-field change = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}
