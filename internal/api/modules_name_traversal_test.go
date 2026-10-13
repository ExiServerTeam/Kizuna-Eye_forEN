package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestModuleDeleteRejectsTraversalName is a regression test for a path
// traversal / arbitrary-delete bug: Go's ServeMux matches {name} against a
// single escaped segment, but %2f is decoded into the value, so name could
// contain "/" and "..". handleDelete builds filepath.Join(pluginsDir,
// name+".web") and calls os.RemoveAll on it, so an unvalidated name such as
// "../../etc" would delete outside the plugins directory.
func TestModuleDeleteRejectsTraversalName(t *testing.T) {
	pluginsDir := t.TempDir()
	// Create a sentinel OUTSIDE pluginsDir that must survive.
	outside := filepath.Join(filepath.Dir(pluginsDir), "victim")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewModuleHandler(NewModulesStorage(filepath.Join(pluginsDir, "modules.json")), pluginsDir)

	for _, name := range []string{"../../etc", "..%2f..%2fvictim", "..", "a/b", "a\\b"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/modules/x", nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		h.handleDelete(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("handleDelete(%q) = %d, want 400", name, rec.Code)
		}
	}

	// The sentinel must still exist.
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Errorf("sentinel outside pluginsDir was removed: %v", err)
	}
}

// TestModuleUpdateRejectsTraversalName mirrors the delete test for update:
// the name reaches ModulesStorage.Update, which matches stored names, so a
// traversal name would never match, but rejecting it early keeps the API
// contract consistent (400, not 404) and avoids surprising callers.
func TestModuleUpdateRejectsTraversalName(t *testing.T) {
	pluginsDir := t.TempDir()
	h := NewModuleHandler(NewModulesStorage(filepath.Join(pluginsDir, "modules.json")), pluginsDir)

	for _, name := range []string{"../../etc", "..", "a/b"} {
		req := httptest.NewRequest(http.MethodPut, "/api/modules/x", strings.NewReader(`{"name":"x"}`))
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		h.handleUpdate(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("handleUpdate(%q) = %d, want 400", name, rec.Code)
		}
	}
}
