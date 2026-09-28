package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newSetupHandler(t *testing.T, authEnabled bool) (*Handler, *Store) {
	t.Helper()
	s := NewStore(filepath.Join(t.TempDir(), "users.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	mgr := NewSessionManager(0, 0)
	t.Cleanup(mgr.Stop)
	return NewHandler(s, mgr, nil, false, authEnabled, ""), s
}

// Regression: while auth is disabled, /api/auth/setup must not create an
// account. Otherwise an attacker could pre-create an admin that becomes valid
// once the operator later enables authentication.
func TestSetupRejectedWhenAuthDisabled(t *testing.T) {
	h, s := newSetupHandler(t, false)

	body := strings.NewReader(`{"username":"attacker","password":"password123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", body)
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("setup while auth disabled: status = %d, want 403", rec.Code)
	}
	if s.Count() != 0 {
		t.Fatalf("no user must be created while auth is disabled, got %d", s.Count())
	}
}

// When auth is enabled and no user exists, setup must still succeed.
func TestSetupAllowedWhenAuthEnabled(t *testing.T) {
	h, s := newSetupHandler(t, true)

	body := strings.NewReader(`{"username":"admin","password":"password123"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", body)
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("setup while auth enabled: status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	if _, ok := s.Authenticate("admin", "password123"); !ok {
		t.Fatal("the first admin should be able to authenticate")
	}
}
