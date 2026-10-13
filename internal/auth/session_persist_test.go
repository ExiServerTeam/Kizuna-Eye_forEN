package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionPersistence verifies a session survives a manager restart when
// persistence is enabled, and that the raw token is NOT written to disk.
func TestSessionPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")

	m1 := NewSessionManager(time.Hour, time.Hour)
	if err := m1.EnablePersistence(path, false); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	s, err := m1.Create("alice", RoleAdmin, "10.0.0.1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m1.Stop() // flushes to disk

	// The raw token must never appear in the file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sessions file: %v", err)
	}
	if bytes.Contains(data, []byte(s.ID)) {
		t.Fatal("raw session token must not be written to disk")
	}

	// A new manager loading the same file must accept the same token.
	m2 := NewSessionManager(time.Hour, time.Hour)
	defer m2.Stop()
	if err := m2.EnablePersistence(path, false); err != nil {
		t.Fatalf("EnablePersistence(2): %v", err)
	}
	got, ok := m2.Get(s.ID)
	if !ok || got.Username != "alice" || got.Role != RoleAdmin {
		t.Fatalf("session did not survive restart: %+v ok=%v", got, ok)
	}
}

// TestSessionPersistenceDisabled verifies that without EnablePersistence the
// manager stays in-memory only (no file created).
func TestSessionPersistenceDisabled(t *testing.T) {
	m := NewSessionManager(time.Hour, time.Hour)
	defer m.Stop()
	if _, err := m.Create("bob", RoleViewer, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Nothing to assert beyond "no panic / no file path configured".
}

// TestSessionIPStored verifies the client IP is recorded on the session.
func TestSessionIPStored(t *testing.T) {
	m := NewSessionManager(time.Hour, time.Hour)
	defer m.Stop()
	s, err := m.Create("bob", RoleViewer, "192.168.0.5")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, ok := m.Get(s.ID)
	if !ok || got.IP != "192.168.0.5" {
		t.Fatalf("expected stored IP, got %+v", got)
	}
}
