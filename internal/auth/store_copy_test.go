package auth

import "testing"

// Regression: Authenticate/Get/Create must return copies of *User, never the
// store's live pointer. A caller that kept the live pointer could race with
// ChangePassword/ChangeRole (which mutate the same struct under the write
// lock), and could also observe or corrupt the stored state.
func TestStoreReturnsUserCopies(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Mutating the result of Authenticate must not change the stored user.
	u, ok := s.Authenticate("admin", "password123")
	if !ok || u == nil {
		t.Fatal("Authenticate failed")
	}
	u.Role = RoleViewer
	u.Username = "hacked"

	u2, ok := s.Authenticate("admin", "password123")
	if !ok {
		t.Fatal("second Authenticate failed")
	}
	if u2.Role != RoleAdmin || u2.Username != "admin" {
		t.Fatalf("stored user was mutated via the returned copy: role=%s name=%s", u2.Role, u2.Username)
	}

	// Get must also return a copy.
	g, ok := s.Get("admin")
	if !ok {
		t.Fatal("Get failed")
	}
	g.Role = RoleViewer
	g2, _ := s.Get("admin")
	if g2.Role != RoleAdmin {
		t.Fatal("Get returned the live pointer (mutation leaked into the store)")
	}

	// Create's return value must be a copy too.
	c, err := s.Create("bob", "password123", RoleOperator)
	if err != nil {
		t.Fatalf("Create bob: %v", err)
	}
	c.Role = RoleViewer
	b, _ := s.Get("bob")
	if b.Role != RoleOperator {
		t.Fatal("Create returned the live pointer (mutation leaked into the store)")
	}
}
