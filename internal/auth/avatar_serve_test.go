package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// Regression: a symlink planted in the avatar directory must not be served
// (it would leak the target file through this public endpoint). The same
// EvalSymlinks + containment check logs.go / plugins.go apply is required
// here too.
func TestHandleAvatarServeRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "evil.png")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	h := &Handler{avatarsDir: dir}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/avatar/evil.png", nil)
	req.SetPathValue("name", "evil.png")
	rec := httptest.NewRecorder()
	h.handleAvatarServe(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("symlink avatar: status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "TOP SECRET") {
		t.Fatal("symlink target leaked through avatar endpoint")
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
