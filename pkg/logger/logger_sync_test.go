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

// Regression: LogFile 未指定（dashboard_config.json の log_file は ""）や
// ログファイルを開けなかった場合、writer に nil の *rotatingWriter が入る。
// 型アサーションは成功してしまうため Sync/Close が nil レシーバを触って
// panic し、ダッシュボードは停止（SIGTERM）のたびにクラッシュしていた
// （logs/dashboard.log に28回の panic を確認）。
func TestLoggerWithoutLogFileSyncCloseDoNotPanic(t *testing.T) {
	lg := NewLogger(&Options{Level: DEBUG})
	if err := lg.Sync(); err != nil {
		t.Fatalf("Sync without a log file must return nil, got %v", err)
	}
	lg.Close()

	// 開けないパス（存在しないディレクトリの下）でも同じこと。
	broken := NewLogger(&Options{
		LogFile: filepath.Join(t.TempDir(), "missing", "dir", "x.log"),
		Level:   DEBUG,
	})
	if err := broken.Sync(); err != nil {
		t.Fatalf("Sync after a failed open must return nil, got %v", err)
	}
	broken.Close()
}
