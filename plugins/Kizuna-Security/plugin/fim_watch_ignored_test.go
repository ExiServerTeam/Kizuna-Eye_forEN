package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// countTitle counts events whose Title equals the ja message for titleKey.
func countTitle(events []module.SecurityEvent, titleKey string) int {
	want := msg("ja", titleKey)
	n := 0
	for _, ev := range events {
		if ev.Title == want {
			n++
		}
	}
	return n
}

// TestFIMIgnoredReportsNewFile: a file matching an ignore pattern is recorded
// once (INFO) so an attacker cannot hide by using *.tmp. Without this the file
// was skipped silently.
func TestFIMIgnoredReportsNewFile(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, []string{"*.tmp"}, 4096, 4096)
	f.Check() // establish the baseline (first scan is silent)
	take()

	evil := filepath.Join(dir, "evil.tmp")
	if err := os.WriteFile(evil, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{evil}, nil)

	events := take()
	if got := countTitle(events, "fimwatch.ignored.title"); got != 1 {
		t.Fatalf("ignored reports = %d, want 1", got)
	}
}

// TestFIMIgnoredUnchangedNotRepeated: re-scanning an unchanged ignored file
// does not report again (no noise).
func TestFIMIgnoredUnchangedNotRepeated(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, []string{"*.tmp"}, 4096, 4096)
	f.Check()
	take()

	evil := filepath.Join(dir, "evil.tmp")
	if err := os.WriteFile(evil, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{evil}, nil)
	take() // first report consumed

	// Same file, unchanged: no second report.
	f.HandleChanges([]string{evil}, nil)
	events := take()
	if got := countTitle(events, "fimwatch.ignored.title"); got != 0 {
		t.Fatalf("unchanged ignored file reported %d times, want 0", got)
	}
}

// TestFIMIgnoredChangedReported: a change to an already-known ignored file is
// reported (an attacker may rewrite a pre-existing ignored path).
func TestFIMIgnoredChangedReported(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, []string{"*.tmp"}, 4096, 4096)
	f.Check()
	take()

	evil := filepath.Join(dir, "evil.tmp")
	if err := os.WriteFile(evil, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{evil}, nil)
	take() // first report consumed

	// Change the file and force a distinct mtime so the signature differs.
	if err := os.WriteFile(evil, []byte("yy"), 0600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(evil, future, future)

	f.HandleChanges([]string{evil}, nil)
	events := take()
	if got := countTitle(events, "fimwatch.ignored.title"); got != 1 {
		t.Fatalf("changed ignored file reported %d times, want 1", got)
	}
}

// TestFIMNonIgnoredStillDetected: a file that does not match any ignore pattern
// is still detected as a new file (the ignore path must not swallow it).
func TestFIMNonIgnoredStillDetected(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, []string{"*.tmp"}, 4096, 4096)
	f.Check()
	take()

	normal := filepath.Join(dir, "normal.txt")
	if err := os.WriteFile(normal, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{normal}, nil)

	events := take()
	if got := countTitle(events, "fimwatch.create.title"); got != 1 {
		t.Fatalf("non-ignored new file: create reports = %d, want 1", got)
	}
	if got := countTitle(events, "fimwatch.ignored.title"); got != 0 {
		t.Fatalf("non-ignored file must not be reported as ignored: %d", got)
	}
}
