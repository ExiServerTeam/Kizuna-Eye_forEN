package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
