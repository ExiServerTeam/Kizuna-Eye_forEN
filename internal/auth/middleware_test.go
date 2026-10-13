package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newTestMiddleware(t *testing.T, authEnabled bool, agentToken string) (*Middleware, *Store) {
	t.Helper()
	s := NewStore(filepath.Join(t.TempDir(), "users.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	mgr := NewSessionManager(0, 0)
	defer mgr.Stop()
	h := NewHandler(s, mgr, nil, false, authEnabled, agentToken)
	return NewMiddleware(h, authEnabled), s
}

func TestMiddlewareDisabledPassesThrough(t *testing.T) {
	m, _ := newTestMiddleware(t, false, "")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	r := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	m.Wrap(next).ServeHTTP(httptest.NewRecorder(), r)
	if !called {
		t.Fatal("with auth disabled, every request should pass through")
	}
}

func TestMiddlewareRejectsUnauthenticatedAPI(t *testing.T) {
	m, s := newTestMiddleware(t, true, "")
	// Create a user so the setup flow does not take over.
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	r := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rec := httptest.NewRecorder()
	m.Wrap(next).ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API = %d, want 401", rec.Code)
	}
}

func TestMiddlewareAllowsAgentToken(t *testing.T) {
	m, s := newTestMiddleware(t, true, "tok123")
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	r := httptest.NewRequest(http.MethodGet, "/ws?role=agent", nil)
	r.Header.Set("X-Kizuna-Agent-Token", "tok123")
	m.Wrap(next).ServeHTTP(httptest.NewRecorder(), r)
	if !called {
		t.Fatal("agent ws connection should bypass session requirement")
	}
}

func TestMiddlewareRedirectsBrowserToLogin(t *testing.T) {
	m, s := newTestMiddleware(t, true, "")
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	m.Wrap(next).ServeHTTP(rec, r)
	if rec.Code != http.StatusFound {
		t.Fatalf("static page = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login.html" {
		t.Fatalf("redirect = %q, want /login.html", loc)
	}
}
