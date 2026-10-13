package alert

import (
	"fmt"
	"testing"
	"time"

	"Kizuna-Eye/pkg/notify"
)

// TestTrimPerTypeCapNoAliasing reproduces a bug where the per-type cap stage
// of trimLocked wrote into the same backing array it was still reading from,
// while iterating newest->oldest. Writing at a lower index than the one being
// read clobbered entries that had not been visited yet, duplicating some
// alerts and losing others.
func TestTrimPerTypeCapNoAliasing(t *testing.T) {
	// max must be >= 4 for the per-type cap stage to run at all.
	h := NewHistory(4)
	base := time.Now()

	// Alternate two types so the per-type cap stage runs (it only runs when
	// more than one Type is present). Distinct messages keep stage 1 (dedup)
	// from collapsing them.
	types := []string{"A", "B", "A", "B", "A", "B"}
	for i, ty := range types {
		h.Add(&notify.Alert{
			Type:      ty,
			Level:     notify.LevelInfo,
			Title:     fmt.Sprintf("t-%d", i),
			Message:   fmt.Sprintf("m-%d", i),
			Timestamp: base.Add(time.Duration(i) * time.Second),
		})
	}

	got := h.List()
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	seen := make(map[string]bool, len(got))
	for _, e := range got {
		key := e.Type + "|" + e.Title
		if seen[key] {
			t.Fatalf("duplicate entry after trim: %s (list=%v)", key, got)
		}
		seen[key] = true
	}
}
