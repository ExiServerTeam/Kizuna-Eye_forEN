package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Regression: a legitimate avatar whose name contains consecutive dots (from a
// username like "a..b") must be served, not rejected as a traversal attempt.
func TestHandleAvatarServeAllowsDottedName(t *testing.T) {
	dir := t.TempDir()
	name := "a..b-deadbeef.png"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := &Handler{avatarsDir: dir}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/avatar/"+name, nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	h.handleAvatarServe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("dotted avatar name: status = %d, want 200", rec.Code)
	}
}

// Path separators and bare dot names must still be rejected.
func TestHandleAvatarServeRejectsTraversal(t *testing.T) {
	h := &Handler{avatarsDir: t.TempDir()}
	for _, name := range []string{"..", ".", "../etc/passwd", "a/b.png", `a\\b.png`} {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/avatar/x", nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		h.handleAvatarServe(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("name %q: status = %d, want 404", name, rec.Code)
		}
	}
}
