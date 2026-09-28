package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadLastLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.log")
	l := &LogHandler{}

	// trailing newline
	if err := os.WriteFile(p, []byte("1\n2\n3\n4\n5\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := l.readLastLines(p, 2); err != nil || got != "4\n5" {
		t.Errorf("trailing: got %q err %v, want %q", got, err, "4\n5")
	}

	// no trailing newline
	if err := os.WriteFile(p, []byte("a\nb\nc"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.readLastLines(p, 2); got != "b\nc" {
		t.Errorf("no-trailing: got %q, want %q", got, "b\nc")
	}

	// fewer lines than requested
	if err := os.WriteFile(p, []byte("x\ny"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.readLastLines(p, 10); got != "x\ny" {
		t.Errorf("fewer: got %q", got)
	}

	// empty file
	if err := os.WriteFile(p, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.readLastLines(p, 5); got != "" {
		t.Errorf("empty: got %q", got)
	}

	// missing file
	if got, err := l.readLastLines(filepath.Join(dir, "nope.log"), 5); err != nil || got != "" {
		t.Errorf("missing: got %q err %v", got, err)
	}
}
