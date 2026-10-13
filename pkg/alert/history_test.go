package alert

import (
	"fmt"
	"testing"
	"time"

	"Kizuna-Eye/pkg/notify"
)

func TestHistoryRingBuffer(t *testing.T) {
	h := NewHistory(3)
	// Distinct messages keep these from collapsing as repeats; the test is
	// about the ring-buffer capacity, not the dedup stage.
	for i := 0; i < 5; i++ {
		h.Add(&notify.Alert{
			Type:      "t",
			Level:     notify.LevelWarning,
			Title:     "a",
			Message:   fmt.Sprintf("event-%d", i),
			Timestamp: time.Unix(int64(i), 0),
		})
	}

	list := h.List()
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	// Newest first (descending): 4,3,2
	if list[0].Timestamp.Unix() != 4 || list[2].Timestamp.Unix() != 2 {
		t.Errorf("order wrong: %v", list)
	}
}

// TestHistoryDedupCollapsesRepeats verifies that a flood of structurally
// identical alerts (the same file changing again and again) is collapsed to
// one entry, so it cannot evict unrelated alerts from a full history.
func TestHistoryDedupCollapsesRepeats(t *testing.T) {
	h := NewHistory(10)
	// First a distinct, important alert.
	h.Add(&notify.Alert{Type: "security_cron", Level: notify.LevelCritical, Title: "cron 変更", Message: "/var/spool/cron/crontabs/user"})
	// Then a flood of the same FIM event, far exceeding the capacity.
	for i := 0; i < 100; i++ {
		h.Add(&notify.Alert{Type: "security_integrity", Level: notify.LevelCritical, Title: "改ざん", Message: "/tmp/noisy.txt が変更されました"})
	}

	list := h.List()
	if len(list) > 10 {
		t.Fatalf("len = %d, want <= 10", len(list))
	}
	var haveCron, integrity int
	for _, e := range list {
		switch e.Type {
		case "security_cron":
			haveCron++
		case "security_integrity":
			integrity++
		}
	}
	if haveCron != 1 {
		t.Errorf("the distinct cron alert must survive the flood: cron=%d integrity=%d", haveCron, integrity)
	}
	if integrity != 1 {
		t.Errorf("the 100 identical integrity alerts must collapse to 1: got %d", integrity)
	}
}

func TestHistoryNilAlertIgnored(t *testing.T) {
	h := NewHistory(10)
	h.Add(nil)
	if h.Len() != 0 {
		t.Errorf("nil alert should be ignored, len=%d", h.Len())
	}
}

func TestNewHistoryDefaultMax(t *testing.T) {
	h := NewHistory(0)
	if h.max != 200 {
		t.Errorf("default max = %d, want 200", h.max)
	}
}
