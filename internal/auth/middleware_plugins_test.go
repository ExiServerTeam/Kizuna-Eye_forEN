package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMinRoleForPlugins(t *testing.T) {
	cases := []struct {
		path string
		want Role
	}{
		{"/plugins/foo/", RoleOperator},
		{"/plugins/foo/index.html", RoleOperator},
		{"/plugins/", RoleOperator},
	}
	for _, c := range cases {
		got, ok := minRoleFor(http.MethodGet, c.path)
		if !ok {
			t.Errorf("minRoleFor(%q): not covered", c.path)
			continue
		}
		if got != c.want {
			t.Errorf("minRoleFor(%q) = %s, want %s", c.path, got, c.want)
		}
	}
}

func TestMiddlewareRejectsViewerOnPlugins(t *testing.T) {
	_, s := newTestMiddleware(t, true, "")
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create admin: %v", err)
	}
	if _, err := s.Create("viewer1", "password123", RoleViewer); err != nil {
		t.Fatalf("Create viewer: %v", err)
	}

	mgr := NewSessionManager(0, 0)
	defer mgr.Stop()
	sess, err := mgr.Create("viewer1", RoleViewer, "")
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	// Rebuild middleware with the session manager that holds the session.
	h := NewHandler(s, mgr, nil, false, true, "")
	mw := NewMiddleware(h, true)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/plugins/foo/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	mw.Wrap(next).ServeHTTP(rec, req)

	if called {
		t.Fatal("viewer must not reach a plugin web UI")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
