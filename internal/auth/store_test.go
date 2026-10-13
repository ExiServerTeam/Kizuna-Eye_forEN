package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "users.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestNeedsSetup(t *testing.T) {
	s := newTestStore(t)
	if !s.NeedsSetup() {
		t.Fatal("empty store should need setup")
	}
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.NeedsSetup() {
		t.Fatal("store with a user should not need setup")
	}
}

func TestCreateAndAuthenticate(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	u, ok := s.Authenticate("admin", "password123")
	if !ok || u == nil {
		t.Fatal("valid credentials should authenticate")
	}
	if u.Role != RoleAdmin {
		t.Fatalf("role = %s, want admin", u.Role)
	}

	if _, ok := s.Authenticate("admin", "wrongpassword"); ok {
		t.Fatal("wrong password must not authenticate")
	}
	if _, ok := s.Authenticate("nobody", "password123"); ok {
		t.Fatal("unknown user must not authenticate")
	}
}

func TestCreateValidation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("ab", "password123", RoleAdmin); err == nil {
		t.Fatal("short username should be rejected")
	}
	if _, err := s.Create("validuser", "short", RoleAdmin); err == nil {
		t.Fatal("short password should be rejected")
	}
	if _, err := s.Create("validuser", "password123", Role("bogus")); err == nil {
		t.Fatal("invalid role should be rejected")
	}
	if _, err := s.Create("validuser", "password123", RoleAdmin); err != nil {
		t.Fatalf("valid create failed: %v", err)
	}
	if _, err := s.Create("validuser", "password123", RoleAdmin); err != ErrUserExists {
		t.Fatalf("duplicate should return ErrUserExists, got %v", err)
	}
}

func TestPasswordNotStoredPlaintext(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if stringContains(string(data), "password123") {
		t.Fatal("plaintext password must not appear in users.json")
	}
}

func TestLastAdminProtection(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete("admin"); err != ErrLastAdmin {
		t.Fatalf("deleting last admin should fail with ErrLastAdmin, got %v", err)
	}
	if err := s.ChangeRole("admin", RoleViewer); err != ErrLastAdmin {
		t.Fatalf("demoting last admin should fail with ErrLastAdmin, got %v", err)
	}

	if _, err := s.Create("admin2", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create admin2: %v", err)
	}
	if err := s.Delete("admin2"); err != nil {
		t.Fatalf("deleting a non-last admin should succeed: %v", err)
	}
}

func TestChangePassword(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ChangePassword("admin", "newpassword456"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, ok := s.Authenticate("admin", "password123"); ok {
		t.Fatal("old password should no longer work")
	}
	if _, ok := s.Authenticate("admin", "newpassword456"); !ok {
		t.Fatal("new password should work")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")

	s1 := NewStore(path)
	if err := s1.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s1.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	s2 := NewStore(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := s2.Authenticate("admin", "password123"); !ok {
		t.Fatal("user should persist across reloads")
	}
}

func TestRoleAtLeast(t *testing.T) {
	if !RoleAdmin.AtLeast(RoleViewer) {
		t.Fatal("admin should satisfy viewer")
	}
	if !RoleOperator.AtLeast(RoleOperator) {
		t.Fatal("operator should satisfy operator")
	}
	if RoleViewer.AtLeast(RoleAdmin) {
		t.Fatal("viewer must not satisfy admin")
	}
}

func stringContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
