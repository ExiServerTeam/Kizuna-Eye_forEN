package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sendFileFromPosition must recover when the file is truncated or rotated.
func TestSendFileFromPositionRecoversAfterRotation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.log")

	if err := os.WriteFile(p, []byte("old line 1\nold line 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &LogHandler{}

	info, _ := os.Stat(p)
	position := info.Size()

	// Rotation: the file is replaced by a smaller one.
	if err := os.WriteFile(p, []byte("new after rotation\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	newPos, err := l.sendFileFromPosition(rr, p, position)
	if err != nil {
		t.Fatalf("sendFileFromPosition: %v", err)
	}
	if !strings.Contains(rr.Body.String(), "new after rotation") {
		t.Errorf("post-rotation line not streamed: %q", rr.Body.String())
	}
	if newPos == position {
		t.Errorf("position should advance after rotation, still %d", newPos)
	}
}

// A normal append must continue from the previous position.
func TestSendFileFromPositionAppend(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.log")
	if err := os.WriteFile(p, []byte("line1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &LogHandler{}

	info, _ := os.Stat(p)
	position := info.Size()

	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("line2\n")
	f.Close()

	rr := httptest.NewRecorder()
	_, err := l.sendFileFromPosition(rr, p, position)
	if err != nil {
		t.Fatalf("sendFileFromPosition: %v", err)
	}
	body := rr.Body.String()
	if strings.Contains(body, "line1") {
		t.Errorf("should not re-send old content: %q", body)
	}
	if !strings.Contains(body, "line2") {
		t.Errorf("new line missing: %q", body)
	}
}
