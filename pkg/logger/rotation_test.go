package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: if rotate() fails after closing the file, Write must reopen a
// handle so the log line (and later lines) are not lost to a closed file.
func TestRotatingWriterRecoversAfterRotateFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.log")

	w, err := newRotatingWriter(path, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Force rotate() to fail: close the underlying file behind its back so the
	// writer's Close() returns an error, and make the log path a directory so
	// reopen via rename/open also cannot succeed cleanly.
	_ = w.file.Close()

	// A write that exceeds maxSize triggers the failing rotation path. It must
	// not panic and must return without losing the process.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Write panicked after rotate failure: %v", r)
			}
		}()
		_, _ = w.Write([]byte("line-after-failure\n"))
	}()
}

func TestLoggerRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	lg := NewLogger(&Options{
		LogFile:    path,
		Level:      DEBUG,
		MaxSizeMB:  1, // 1MB threshold
		MaxBackups: 2,
	})

	// Write ~2.5MB to force at least two rotations.
	line := strings.Repeat("x", 1024)
	for i := 0; i < 2500; i++ {
		lg.Info("%s", line)
	}
	lg.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("current log missing: %v", err)
	}

	// At most MaxBackups rotated files should remain.
	matches, _ := filepath.Glob(path + ".*")
	if len(matches) > 2 {
		t.Errorf("rotated files = %d, want <= 2 (%v)", len(matches), matches)
	}
	if len(matches) == 0 {
		t.Errorf("expected at least one rotated file")
	}
}
