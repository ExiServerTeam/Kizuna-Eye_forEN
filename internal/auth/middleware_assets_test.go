package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// newAssetTestMiddleware builds a middleware with auth enabled and no session,
// so isPublicPath is the only thing that can let a request through.
func newAssetTestMiddleware(t *testing.T) *Middleware {
	t.Helper()
	s := NewStore(filepath.Join(t.TempDir(), "users.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create admin: %v", err)
	}
	mgr := NewSessionManager(0, 0)
	t.Cleanup(mgr.Stop)
	h := NewHandler(s, mgr, nil, false, true, "")
	return NewMiddleware(h, true)
}

func requestAsset(mw *Middleware, path string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw.Wrap(next).ServeHTTP(rec, req)
	return rec.Code
}

// Static assets must load before a session exists (login/setup/public viewer).
// A missing extension here breaks the UI silently: the request is redirected to
// the login page instead of returning the file.
func TestPublicStaticAssetExtensions(t *testing.T) {
	mw := newAssetTestMiddleware(t)
	for _, p := range []string{
		"/redesign.css",
		"/app.js",
		"/i18n.js",
		"/logo.svg",
		"/fonts/LogotypeGothic.otf",
		"/fonts/x.woff2",
		"/fonts/x.ttf",
		"/favicon.ico",
	} {
		if got := requestAsset(mw, p); got != http.StatusOK {
			t.Errorf("unauthenticated GET %s = %d, want 200 (static asset must be public)", p, got)
		}
	}
}

// A non-asset path still requires a session (no accidental allow-all).
func TestNonAssetStillGated(t *testing.T) {
	mw := newAssetTestMiddleware(t)
	if got := requestAsset(mw, "/users.html"); got != http.StatusFound {
		t.Errorf("unauthenticated GET /users.html = %d, want 302", got)
	}
}
