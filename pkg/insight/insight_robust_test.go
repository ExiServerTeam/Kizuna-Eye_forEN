package insight

import (
	"strings"
	"testing"
	"time"

	"Kizuna-Eye/pkg/alert"
)

// TestExtractIPsHugeInputMustNotHang guards against pathological input: a
// multi-megabyte line must be parsed in bounded time. ExtractIPs is a linear
// scanner (no regexp backtracking) and parses each candidate once, so even
// under `go test -race` (2-3x slower) a 5 MB input finishes quickly. The
// threshold is generous (10s) so a slow CI machine does not produce a false
// failure; a real hang would blow past it.
func TestExtractIPsHugeInputMustNotHang(t *testing.T) {
	s := strings.Repeat("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 200000) // ~5.6 MB
	done := make(chan struct{})
	go func() {
		_ = ExtractIPs(s)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ExtractIPs did not finish within 10s on a 5MB input")
	}
}

// A run of hex letters that is not an IP must not be reported; a real address
// bounded by non-IP characters is found.
//
// Note: "x1.2.3.4y" DOES match (1.2.3.4 is bounded by x/y, which are not IP
// bytes). Only a fragment cut out of a larger hex token (e.g. "abc1.2.3.4",
// where the byte before the run is a hex digit) is skipped.
func TestExtractIPsHexRunAndEmbedded(t *testing.T) {
	if got := ExtractIPs(strings.Repeat("abc", 1000)); len(got) != 0 {
		t.Fatalf("hex letter run must not yield an IP, got %v", got)
	}
	if got := ExtractIPs("addr=10.0.0.1"); len(got) != 1 || got[0] != "10.0.0.1" {
		t.Fatalf("embedded IPv4 not found: %v", got)
	}
	// Glued to non-hex letters: still a valid address.
	if got := ExtractIPs("x1.2.3.4y"); len(got) != 1 || got[0] != "1.2.3.4" {
		t.Fatalf("address bounded by letters should match, got %v", got)
	}
	// Glued directly to a hex digit: a fragment, not an address.
	if got := ExtractIPs("abc1.2.3.4"); len(got) != 0 {
		t.Fatalf("address glued to a hex digit must not match, got %v", got)
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
