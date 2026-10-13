package api

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// TestReadLineCappedNormalLine reads a short, newline-terminated line.
func TestReadLineCappedNormalLine(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("hello\nworld\n"))
	ln, err := readLineCapped(r, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if ln.text != "hello\n" || ln.consumed != 6 || !ln.complete {
		t.Fatalf("got %+v", ln)
	}
}

// TestReadLineCappedHugeLine does not allocate the whole line and consumes
// exactly up to and including the terminating newline.
func TestReadLineCappedHugeLine(t *testing.T) {
	// 10 MiB without a newline, then a newline.
	huge := strings.Repeat("A", 10<<20)
	r := bufio.NewReader(strings.NewReader(huge + "\n"))
	ln, err := readLineCapped(r, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(ln.text) > 1024 {
		t.Fatalf("capped line kept %d bytes, want <= 1024", len(ln.text))
	}
	if ln.consumed != int64(len(huge))+1 {
		t.Fatalf("consumed = %d, want %d", ln.consumed, len(huge)+1)
	}
	if !ln.complete {
		t.Fatal("line should be complete (newline drained)")
	}
	// The reader must be positioned right after the newline.
	if _, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("expected EOF after the drained line, got %v", err)
	}
}

// TestReadLineCappedEOFWithoutNewline reports an incomplete line at EOF.
func TestReadLineCappedEOFWithoutNewline(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("partial"))
	ln, err := readLineCapped(r, 1024)
	if err != io.EOF {
		t.Fatalf("err = %v, want EOF", err)
	}
	if ln.complete {
		t.Fatal("line without newline must not be complete")
	}
	if ln.text != "partial" {
		t.Fatalf("text = %q", ln.text)
	}
}
