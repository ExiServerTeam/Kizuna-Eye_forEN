package alert

import (
	"path/filepath"
	"testing"
	"time"

	"Kizuna-Eye/pkg/notify"
)

func TestHistoryPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alerts.jsonl")

	h := NewHistory(10)
	h.SetPersistence(path)
	h.Add(&notify.Alert{Type: "memory", Level: notify.LevelCritical, Title: "t1", Timestamp: time.Now()})
	h.Add(&notify.Alert{Type: "disk", Level: notify.LevelWarning, Title: "t2", Timestamp: time.Now()})

	// Reload into a fresh History.
	h2 := NewHistory(10)
	if err := h2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if h2.Len() != 2 {
		t.Fatalf("reloaded len = %d, want 2", h2.Len())
	}
	list := h2.List()
	if list[0].Title != "t2" {
		t.Errorf("newest first expected t2, got %q", list[0].Title)
	}
}

func TestHistoryLoadMissingFile(t *testing.T) {
	h := NewHistory(10)
	if err := h.Load(filepath.Join(t.TempDir(), "nope.jsonl")); err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
}
