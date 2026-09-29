package auth

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// When persistence fails (unwritable path), the configured logger must be
// called instead of failing silently.
func TestSessionPersistenceLogsFailure(t *testing.T) {
	// Point the session file at a path whose parent is a regular file, not a
	// directory, so MkdirAll/CreateTemp fails.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	path := filepath.Join(blocker, "sessions.json") // parent is a file

	m := NewSessionManager(time.Hour, time.Hour)
	defer m.Stop()

	var mu sync.Mutex
	logged := 0
	m.SetLogger(func(format string, args ...interface{}) {
		mu.Lock()
		logged++
		mu.Unlock()
	})
	_ = m.EnablePersistence(path, false)
	if _, err := m.Create("alice", RoleAdmin, ""); err != nil {
		t.Fatalf("Create: %v", err)
	}

	mu.Lock()
	n := logged
	mu.Unlock()
	if n == 0 {
		t.Fatal("expected the persistence logger to be called on failure")
	}
}
