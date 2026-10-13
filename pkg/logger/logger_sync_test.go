package logger

import (
	"path/filepath"
	"sync"
	"testing"
)

// Regression: Sync/Close must take the logger mutex so they cannot race with
// a concurrent log write touching the same file/writer. Run with -race to
// detect the previous unlocked access.
func TestLoggerSyncCloseRaceFree(t *testing.T) {
	dir := t.TempDir()
	lg := NewLogger(&Options{
		LogFile: filepath.Join(dir, "test.log"),
		Level:   DEBUG,
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			lg.Info("line %d", i)
		}
	}()

	for i := 0; i < 50; i++ {
		_ = lg.Sync()
	}
	wg.Wait()
	lg.Close()
}
