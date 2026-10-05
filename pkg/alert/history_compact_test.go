package alert

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"Kizuna-Eye/pkg/notify"
)

// Loading a file with more entries than max must trim AND compact it.
func TestHistoryLoadCompacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alerts.jsonl")

	// Write 50 entries with a small max of 10.
	h := NewHistory(10)
	h.SetPersistence(path)
	// Distinct messages keep these from collapsing as repeats; this test is
	// about persistence/compaction, not the dedup stage.
	for i := 0; i < 50; i++ {
		h.Add(&notify.Alert{Type: "t", Level: notify.LevelInfo, Title: "x", Message: fmt.Sprintf("e-%d", i), Timestamp: time.Now()})
	}

	// The file may have grown past max; reload into a fresh History.
	h2 := NewHistory(10)
	if err := h2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if h2.Len() != 10 {
		t.Fatalf("len = %d, want 10", h2.Len())
	}

	// After Load the file should be compacted to at most 10 lines.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines > 10 {
		t.Errorf("file not compacted: %d lines, want <= 10", lines)
	}
}
