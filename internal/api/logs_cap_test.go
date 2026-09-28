package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A huge file with no newlines must not be read in full: readLastLines caps
// the amount buffered, so memory stays bounded.
func TestReadLastLinesCapsMemoryOnNewlineFreeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.log")

	// 8 MiB with no newline characters at all.
	const size = 8 << 20
	data := strings.Repeat("A", size)
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	l := &LogHandler{}
	got, err := l.readLastLines(p, 100)
	if err != nil {
		t.Fatalf("readLastLines: %v", err)
	}
	// The cap is 4 MiB; the returned content must not exceed that.
	if len(got) > 4<<20 {
		t.Fatalf("readLastLines returned %d bytes, want <= %d", len(got), 4<<20)
	}
}

// The normal case still works: the last N lines of a small file are returned.
func TestReadLastLinesNormal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "small.log")
	if err := os.WriteFile(p, []byte("l1\nl2\nl3\nl4\nl5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &LogHandler{}
	got, err := l.readLastLines(p, 2)
	if err != nil {
		t.Fatalf("readLastLines: %v", err)
	}
	if got != "l4\nl5" {
		t.Fatalf("readLastLines = %q, want %q", got, "l4\nl5")
	}
}
