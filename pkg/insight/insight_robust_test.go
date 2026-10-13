package insight

import (
	"strings"
	"testing"
	"time"

	"Kizuna-Eye/pkg/alert"
)

// TestExtractIPsHugeInputMustNotHang guards against pathological regexp
// behavior. Go's regexp is RE2 (linear time), so this must complete quickly
// even on a long non-matching string.
func TestExtractIPsHugeInputMustNotHang(t *testing.T) {
	s := strings.Repeat("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 200000) // ~5.6 MB
	done := make(chan struct{})
	go func() {
		_ = ExtractIPs(s)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ExtractIPs did not finish within 5s on a 5MB input")
	}
}

// TestExtractIPsMixed covers ports, ipv6, and the dropped bind address.
func TestExtractIPsMixed(t *testing.T) {
	in := "from 127.0.0.1:22 and ::1 and 8.8.8.8 and 0.0.0.0"
	got := ExtractIPs(in)
	if len(got) != 3 { // ::1, 127.0.0.1, 8.8.8.8 (0.0.0.0 dropped)
		t.Fatalf("got %v, want 3 addresses", got)
	}
}

// TestAnalyzeManyEntriesDoesNotMutate ensures Analyze copies out of the input
// slice and never writes back to the caller's entries.
func TestAnalyzeManyEntriesDoesNotMutate(t *testing.T) {
	now := time.Now()
	entries := make([]alert.HistoryEntry, 0, 1000)
	for i := 0; i < 1000; i++ {
		entries = append(entries, alert.HistoryEntry{
			Type:      "security_ssh_failed",
			Level:     "warning",
			Message:   "from 10.0.0.1",
			Timestamp: now.Add(-time.Duration(i) * time.Second),
		})
	}
	before := entries[0].Timestamp
	_ = Analyze(entries, now, 24*time.Hour)
	if !entries[0].Timestamp.Equal(before) {
		t.Fatal("Analyze mutated the input slice")
	}
}
