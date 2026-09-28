package alert

import (
	"testing"
	"time"

	"Kizuna-Eye/pkg/notify"
)

func TestHistoryRingBuffer(t *testing.T) {
	h := NewHistory(3)
	for i := 0; i < 5; i++ {
		h.Add(&notify.Alert{
			Type:      "t",
			Level:     notify.LevelWarning,
			Title:     "a",
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
