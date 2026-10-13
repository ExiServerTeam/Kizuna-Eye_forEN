package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestModuleDeleteRemovesSignature verifies that deleting a module through the
// modules API also removes the plugin's detached signature (<name>.so.sig).
//
// The plugin-upload delete path (PluginManager.handleDelete) removes the .sig
// on purpose: a stale signature left next to a later upload of a different .so
// under the same name makes signature verification fail (the file is signed
// with the old key/hash), so the plugin silently refuses to load. The modules
// delete path must do the same.
func TestModuleDeleteRemovesSignature(t *testing.T) {
	dir := t.TempDir()

	soPath := filepath.Join(dir, "foo.so")
	if err := os.WriteFile(soPath, []byte("ELF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo.meta.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "foo.so.sig")
	if err := os.WriteFile(sigPath, []byte("c2ln"), 0o644); err != nil {
		t.Fatal(err)
	}

	storage := NewModulesStorage(filepath.Join(dir, "modules.json"))
	cfg, _ := json.Marshal(map[string]string{"plugin_path": soPath})
	if err := storage.Add(ModuleConfig{Name: "foo", Type: "plugin", Enabled: true, Config: cfg}); err != nil {
		t.Fatal(err)
	}

	h := NewModuleHandler(storage, dir)
	req := httptest.NewRequest(http.MethodDelete, "/api/modules/foo", nil)
	req.SetPathValue("name", "foo")
	rec := httptest.NewRecorder()
	h.handleDelete(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(sigPath); !os.IsNotExist(err) {
		t.Fatalf("detached signature %s was not removed (err=%v)", sigPath, err)
	}
}
