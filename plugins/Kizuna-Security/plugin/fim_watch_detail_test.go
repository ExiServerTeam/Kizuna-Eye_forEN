package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestFIMSummaryKeepsAllPaths: when a scan produces more changes than the
// per-scan notification cap, the individual events are capped but every
// suppressed path is still recorded (as an info detail event) so it remains
// traceable. This is the regression test for the attack test finding that only
// 20 of 1000 files were recorded.
func TestFIMSummaryKeepsAllPaths(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, nil, 4096, 4096)
	f.Check() // baseline (silent)
	take()

	const n = 100
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("file%03d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.Check()
	events := take()

	// The individual change events are capped at fimDirEventCap, and one
	// summary event is appended.
	changeCount := countTitle(events, "fimwatch.create.title")
	if changeCount != fimDirEventCap {
		t.Errorf("create events = %d, want %d (cap)", changeCount, fimDirEventCap)
	}
	if got := countTitle(events, "fimwatch.many.title"); got != 1 {
		t.Errorf("summary events = %d, want 1", got)
	}
	// Every suppressed path must appear as a detail event (info, not notified).
	detailCount := countTitle(events, "fimwatch.detail.title")
	wantDetail := n - fimDirEventCap
	if detailCount != wantDetail {
		t.Errorf("detail events = %d, want %d (all suppressed paths)", detailCount, wantDetail)
	}
	for _, ev := range events {
		if ev.Title == msg("ja", "fimwatch.detail.title") && ev.Level != "info" {
			t.Errorf("detail event must be info level: %+v", ev)
		}
	}
}

// TestFIMSummaryDetailCap: an extreme burst is bounded by fimSummaryDetailCap
// and raises a warning, so the trace log itself cannot become the flood.
func TestFIMSummaryDetailCap(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, nil, 4096, 4096)
	f.Check()
	take()

	// Create more than fimSummaryDetailCap + fimDirEventCap files so the
	// detail collector is forced past its cap.
	total := fimSummaryDetailCap + fimDirEventCap + 50
	for i := 0; i < total; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%05d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.Check()
	events := take()

	detailCount := countTitle(events, "fimwatch.detail.title")
	if detailCount != fimSummaryDetailCap {
		t.Errorf("detail events = %d, want %d (cap)", detailCount, fimSummaryDetailCap)
	}
	if got := countTitle(events, "fimwatch.detail.cap.title"); got != 1 {
		t.Errorf("detail cap warning = %d, want 1", got)
	}
}

// TestFIMSummaryDetailNotNotified: the detail events are info level, so the
// notification queue (NotifyMinimal) does not include them. The notifier must
// still see only the capped events + summary.
func TestFIMSummaryDetailIsInfoLevel(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, nil, 4096, 4096)
	f.Check()
	take()

	for i := 0; i < fimDirEventCap+10; i++ {
		p := filepath.Join(dir, fmt.Sprintf("g%03d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.Check()
	events := take()

	for _, ev := range events {
		if ev.Title == msg("ja", "fimwatch.detail.title") {
			if ev.Level != "info" {
				t.Fatalf("detail event level = %q, want info", ev.Level)
			}
			if ev.Category != "integrity" {
				t.Fatalf("detail event category = %q, want integrity", ev.Category)
			}
		}
	}
}
