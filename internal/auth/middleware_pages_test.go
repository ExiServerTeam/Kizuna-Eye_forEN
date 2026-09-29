package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// newPageTestHandler builds a handler + a session for the given role.
func newPageTestHandler(t *testing.T, role Role) (*Middleware, string) {
	t.Helper()
	s := NewStore(filepath.Join(t.TempDir(), "users.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Create at least one user so NeedsSetup() is false; otherwise the
	// middleware redirects everything to /setup.html before page gating.
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create admin: %v", err)
	}
	mgr := NewSessionManager(0, 0)
	t.Cleanup(mgr.Stop)
	h := NewHandler(s, mgr, nil, false, true, "")
	sess, err := mgr.Create("u", role, "")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	return NewMiddleware(h, true), sess.ID
}

func requestPage(mw *Middleware, sid, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if sid != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: sid})
	}
	rec := httptest.NewRecorder()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw.Wrap(next).ServeHTTP(rec, req)
	return rec
}

// A viewer must not be able to load the admin/operator page shells.
func TestStaticPageGatingViewer(t *testing.T) {
	mw, sid := newPageTestHandler(t, RoleViewer)
	for _, p := range []string{"/users.html", "/config-editor.html", "/logs.html", "/modules.html"} {
		rec := requestPage(mw, sid, p)
		if rec.Code != http.StatusFound {
			t.Errorf("viewer GET %s = %d, want 302", p, rec.Code)
		}
	}
	// The dashboard itself is allowed for a logged-in viewer.
	if rec := requestPage(mw, sid, "/"); rec.Code != http.StatusOK {
		t.Errorf("viewer GET / = %d, want 200", rec.Code)
	}
}

// An operator may open the logs/modules shells but not the admin ones.
func TestStaticPageGatingOperator(t *testing.T) {
	mw, sid := newPageTestHandler(t, RoleOperator)
	for _, p := range []string{"/logs.html", "/modules.html"} {
		if rec := requestPage(mw, sid, p); rec.Code != http.StatusOK {
			t.Errorf("operator GET %s = %d, want 200", p, rec.Code)
		}
	}
	for _, p := range []string{"/users.html", "/config-editor.html"} {
		if rec := requestPage(mw, sid, p); rec.Code != http.StatusFound {
			t.Errorf("operator GET %s = %d, want 302", p, rec.Code)
		}
	}
}

// An admin may open every gated page.
func TestStaticPageGatingAdmin(t *testing.T) {
	mw, sid := newPageTestHandler(t, RoleAdmin)
	for _, p := range []string{"/users.html", "/config-editor.html", "/logs.html", "/modules.html"} {
		if rec := requestPage(mw, sid, p); rec.Code != http.StatusOK {
			t.Errorf("admin GET %s = %d, want 200", p, rec.Code)
		}
	}
}
