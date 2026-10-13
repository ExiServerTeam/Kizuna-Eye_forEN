package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Clearing the alert history destroys the audit trail, so it is admin-only;
// reading the history stays viewer-accessible.
func TestMinRoleForAlertClear(t *testing.T) {
	if got, ok := minRoleFor(http.MethodDelete, "/api/alerts"); !ok || got != RoleAdmin {
		t.Fatalf("minRoleFor(DELETE, /api/alerts) = %s, %v; want admin, true", got, ok)
	}
	if got, ok := minRoleFor(http.MethodGet, "/api/alerts"); !ok || got != RoleViewer {
		t.Fatalf("minRoleFor(GET, /api/alerts) = %s, %v; want viewer, true", got, ok)
	}
}

// Plugin metadata listing and manual backup runs are part of the modules
// page, which operators may use; upload and delete stay admin-only.
func TestMinRoleForPluginPaths(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   Role
	}{
		{http.MethodGet, "/api/plugins", RoleOperator},
		{http.MethodPost, "/api/plugins/kizuna_backup_lite/run", RoleOperator},
		{http.MethodPost, "/api/plugins/upload", RoleAdmin},
		{http.MethodDelete, "/api/plugins/kizuna_backup_lite", RoleAdmin},
	}
	for _, c := range cases {
		got, ok := minRoleFor(c.method, c.path)
		if !ok {
			t.Errorf("minRoleFor(%s, %s): not covered", c.method, c.path)
			continue
		}
		if got != c.want {
			t.Errorf("minRoleFor(%s, %s) = %s, want %s", c.method, c.path, got, c.want)
		}
	}
}

func TestMiddlewareRejectsViewerOnDeleteAlerts(t *testing.T) {
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

	h := NewHandler(s, mgr, nil, false, true, "")
	mw := NewMiddleware(h, true)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodDelete, "/api/alerts", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	mw.Wrap(next).ServeHTTP(rec, req)

	if called {
		t.Fatal("viewer must not reach DELETE /api/alerts")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// An operator must NOT be able to clear the alert history (admin-only).
func TestMiddlewareRejectsOperatorOnDeleteAlerts(t *testing.T) {
	_, s := newTestMiddleware(t, true, "")
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create admin: %v", err)
	}
	if _, err := s.Create("op1", "password123", RoleOperator); err != nil {
		t.Fatalf("Create operator: %v", err)
	}

	mgr := NewSessionManager(0, 0)
	defer mgr.Stop()
	sess, err := mgr.Create("op1", RoleOperator, "")
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	h := NewHandler(s, mgr, nil, false, true, "")
	mw := NewMiddleware(h, true)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodDelete, "/api/alerts", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	mw.Wrap(next).ServeHTTP(rec, req)

	if called {
		t.Fatal("operator must not reach DELETE /api/alerts")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// An admin must be able to clear the alert history.
func TestMiddlewareAllowsAdminOnDeleteAlerts(t *testing.T) {
	_, s := newTestMiddleware(t, true, "")
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create admin: %v", err)
	}

	mgr := NewSessionManager(0, 0)
	defer mgr.Stop()
	sess, err := mgr.Create("admin", RoleAdmin, "")
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	h := NewHandler(s, mgr, nil, false, true, "")
	mw := NewMiddleware(h, true)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodDelete, "/api/alerts", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	mw.Wrap(next).ServeHTTP(rec, req)

	if !called {
		t.Fatalf("admin should reach DELETE /api/alerts (status=%d)", rec.Code)
	}
}
