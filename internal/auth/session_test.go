package auth

import (
	"testing"
	"time"
)

func TestSessionCreateAndGet(t *testing.T) {
	m := NewSessionManager(time.Hour, time.Hour)
	defer m.Stop()

	s, err := m.Create("alice", RoleAdmin, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, ok := m.Get(s.ID)
	if !ok || got.Username != "alice" || got.Role != RoleAdmin {
		t.Fatalf("Get returned %+v, ok=%v", got, ok)
	}
}

func TestSessionCapEvictsGuestFirst(t *testing.T) {
	m := NewSessionManager(time.Hour, time.Hour)
	defer m.Stop()
	m.max = 3

	// Fill with one admin and one guest.
	if _, err := m.Create("admin", RoleAdmin, ""); err != nil {
		t.Fatalf("Create admin: %v", err)
	}
	guest, err := m.Create("guest", RoleViewer, "")
	if err != nil {
		t.Fatalf("Create guest: %v", err)
	}
	if _, err := m.Create("op", RoleOperator, ""); err != nil {
		t.Fatalf("Create op: %v", err)
	}

	// A 4th session must succeed by evicting the guest, and the guest must
	// no longer be retrievable.
	if _, err := m.Create("guest2", RoleViewer, ""); err != nil {
		t.Fatalf("Create 4th: %v", err)
	}
	if _, ok := m.Get(guest.ID); ok {
		t.Fatal("guest session should have been evicted")
	}
	if len(m.sessions) > m.max {
		t.Fatalf("session count %d exceeds cap %d", len(m.sessions), m.max)
	}
}

func TestSessionDelete(t *testing.T) {
	m := NewSessionManager(time.Hour, time.Hour)
	defer m.Stop()

	s, _ := m.Create("bob", RoleViewer, "")
	m.Delete(s.ID)
	if _, ok := m.Get(s.ID); ok {
		t.Fatal("deleted session should not be retrievable")
	}
}

func TestSessionExpiry(t *testing.T) {
	// ttl very short so it expires immediately.
	m := NewSessionManager(time.Millisecond, time.Hour)
	defer m.Stop()

	s, _ := m.Create("carol", RoleViewer, "")
	time.Sleep(5 * time.Millisecond)
	if _, ok := m.Get(s.ID); ok {
		t.Fatal("expired session should not be retrievable")
	}
}

func TestDeleteUserSessions(t *testing.T) {
	m := NewSessionManager(time.Hour, time.Hour)
	defer m.Stop()

	s1, _ := m.Create("dave", RoleViewer, "")
	s2, _ := m.Create("dave", RoleViewer, "")
	s3, _ := m.Create("erin", RoleViewer, "")

	m.DeleteUserSessions("dave")
	if _, ok := m.Get(s1.ID); ok {
		t.Fatal("dave session 1 should be removed")
	}
	if _, ok := m.Get(s2.ID); ok {
		t.Fatal("dave session 2 should be removed")
	}
	if _, ok := m.Get(s3.ID); !ok {
		t.Fatal("erin session should remain")
	}
}

func TestSessionIDsAreUnique(t *testing.T) {
	m := NewSessionManager(time.Hour, time.Hour)
	defer m.Stop()

	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s, _ := m.Create("user", RoleViewer, "")
		if seen[s.ID] {
			t.Fatal("duplicate session ID generated")
		}
		seen[s.ID] = true
	}
}
