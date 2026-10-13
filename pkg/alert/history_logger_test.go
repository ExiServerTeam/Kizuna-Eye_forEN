package alert

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"Kizuna-Eye/pkg/notify"
)

// When the history file cannot be written, the configured logger must be
// called instead of failing silently.
func TestHistoryPersistenceLogsFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	path := filepath.Join(blocker, "alerts.jsonl") // parent is a file

	h := NewHistory(10)
	var mu sync.Mutex
	logged := 0
	h.SetLogger(func(format string, args ...interface{}) {
		mu.Lock()
		logged++
		mu.Unlock()
	})
	h.SetPersistence(path)
	h.Add(&notify.Alert{Type: "t", Level: notify.LevelWarning, Title: "x", Message: "y", Timestamp: time.Now()})

	mu.Lock()
	n := logged
	mu.Unlock()
	if n == 0 {
		t.Fatal("expected the history logger to be called on failure")
	}
}
