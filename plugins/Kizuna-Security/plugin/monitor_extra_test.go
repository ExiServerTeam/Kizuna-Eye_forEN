package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// 改行で終わっていない「書きかけの最終行」は消費しないことを検証する。
func TestScanFileKeepsPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.log")
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.WatchFiles = []string{path}
	cfg.NotifyMinimal = "info" // SSHログイン成功(info)もキューに積む
	m := NewMonitor(cfg, nil, nil)
	m.loginKnownIPs["1.2.3.4"] = true

	// 監視開始（空ファイルなのでオフセットは0）。
	m.Scan()

	// 末尾に改行がない行を書く。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("Accepted password for alice from 1.2.3.4 port 22 ssh2")
	_ = f.Close()

	m.Scan()
	if got := len(m.Drain()); got != 0 {
		t.Fatalf("partial line should not be processed, got %d events", got)
	}

	// 改行を追加すると、その行が処理される。
	f, _ = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	_, _ = f.WriteString("\n")
	_ = f.Close()

	m.Scan()
	if got := len(m.Drain()); got != 1 {
		t.Fatalf("completed line should be processed, got %d events", got)
	}
}

// 監視対象パスの検証。
func TestValidateWatchPath(t *testing.T) {
	bad := []string{
		"",
		"relative/auth.log",
		"/var/log/../etc/passwd.log",
		"/var/log/auth.txt",
		"/var/log/auth.log\n",
	}
	for _, p := range bad {
		if err := validateWatchPath(p); err == nil {
			t.Errorf("validateWatchPath(%q) should fail", p)
		}
	}
	if err := validateWatchPath("/var/log/auth.log"); err != nil {
		t.Errorf("valid path rejected: %v", err)
	}
}

// 出力ログパスの検証。
func TestValidateOutputPath(t *testing.T) {
	if err := validateOutputPath("./logs/kizuna-security.log"); err != nil {
		t.Errorf("valid output path rejected: %v", err)
	}
	for _, p := range []string{"", "./logs/x.txt", "./logs/../secret.log"} {
		if err := validateOutputPath(p); err == nil {
			t.Errorf("validateOutputPath(%q) should fail", p)
		}
	}
}

// しきい値到達後、カウンタが上限を超えないことを検証する。
func TestFailTrackerEviction(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FailedBurst = 5
	cfg.BurstWindow = 60
	m := NewMonitor(cfg, nil, nil)

	// 上限を超える件数の異なるIPを処理する。
	now := time.Now()
	for i := 0; i < maxFailTrackers+100; i++ {
		ip := "10.0." + itoa(i/256) + "." + itoa(i%256)
		m.emitSSHFailure("/var/log/auth.log", "user", ip, now)
	}
	if len(m.failCounts) > maxFailTrackers {
		t.Fatalf("failCounts grew beyond limit: %d", len(m.failCounts))
	}
}

// キューが上限を超えないことを検証する。
func TestQueueLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NotifyMinimal = "info"
	m := NewMonitor(cfg, nil, nil)

	for i := 0; i < maxQueueSize+500; i++ {
		m.emit(module.SecurityEvent{Level: "info", Category: "test", Title: "t"})
	}
	if len(m.queue) > maxQueueSize {
		t.Fatalf("queue grew beyond limit: %d", len(m.queue))
	}
}
