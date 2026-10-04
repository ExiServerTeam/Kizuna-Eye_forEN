package main

import (
	"strings"
	"testing"
	"time"
)

func newTestMonitor(minLevel string) *Monitor {
	cfg := DefaultConfig()
	cfg.NotifyMinimal = minLevel
	// Do not touch the shared on-disk login baseline: tests run in the
	// package directory, so the default relative path points at one shared
	// file. A test that records a "known IP" there would make a later test
	// (which expects that IP to be unknown) fail depending on run order.
	cfg.SSHLoginBaselinePath = ""
	return NewMonitor(cfg, nil, nil)
}

func firstEvent(t *testing.T, m *Monitor) []struct{} { return nil }

func TestSSHLoginDetected(t *testing.T) {
	m := newTestMonitor("info")
	m.loginKnownIPs["1.2.3.4"] = true
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Accepted password for alice from 1.2.3.4 port 50000 ssh2",
		time.Now())
	events := m.Drain()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Category != "ssh_login" || events[0].Actor != "alice" || events[0].IP != "1.2.3.4" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
	if events[0].Level != "info" {
		t.Errorf("level = %s, want info", events[0].Level)
	}
}

func TestSSHFailedWarning(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Failed password for bob from 5.6.7.8 port 50000 ssh2",
		time.Now())
	events := m.Drain()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Category != "ssh_failed" || events[0].Level != "warning" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
}

func TestSSHFailedBurstCritical(t *testing.T) {
	m := newTestMonitor("warning")
	m.cfg.FailedBurst = 3
	m.cfg.BurstWindow = 60

	// 同一IPからの別ユーザーでの失敗を数える（総当たり攻撃を想定）。
	now := time.Now()
	for i := 0; i < 3; i++ {
		line := "Sep 27 08:00:42 server1 sshd[1]: Failed password for user" + itoa(i) + " from 9.9.9.9 port 50000 ssh2"
		m.classify("/var/log/auth.log", line, now.Add(time.Duration(i)*time.Second))
	}
	events := m.Drain()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	// 3回目でしきい値に到達し CRITICAL になる。
	if events[2].Level != "critical" {
		t.Errorf("3rd level = %s, want critical", events[2].Level)
	}
}

func TestSSHFailedDedupSameAttempt(t *testing.T) {
	m := newTestMonitor("warning")
	now := time.Now()
	// 1回の試行が複数行を残しても、同一 user@ip の連続行は1件にまとめる。
	lines := []string{
		"Sep 27 08:00:42 server1 sshd[1]: Invalid user bob from 9.9.9.9 port 50000",
		"Sep 27 08:00:42 server1 sshd[1]: Failed password for invalid user bob from 9.9.9.9 port 50000 ssh2",
		"Sep 27 08:00:42 server1 sshd[1]: Connection closed by authenticating user bob 9.9.9.9 port 50000 [preauth]",
	}
	for _, line := range lines {
		m.classify("/var/log/auth.log", line, now)
	}
	events := m.Drain()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (deduplicated)", len(events))
	}
}

func TestSSHFailedCriticalNotRepeated(t *testing.T) {
	m := newTestMonitor("warning")
	m.cfg.FailedBurst = 3
	m.cfg.BurstWindow = 60

	now := time.Now()
	criticalCount := 0
	for i := 0; i < 6; i++ {
		line := "Sep 27 08:00:42 server1 sshd[1]: Failed password for user" + itoa(i) + " from 9.9.9.9 port 50000 ssh2"
		m.classify("/var/log/auth.log", line, now.Add(time.Duration(i)*time.Second))
	}
	for _, ev := range m.Drain() {
		if ev.Level == "critical" {
			criticalCount++
		}
	}
	if criticalCount != 1 {
		t.Errorf("critical count = %d, want 1", criticalCount)
	}
}

func TestInvalidUserDetected(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Invalid user admin from 1.2.3.4 port 50000",
		time.Now())
	events := m.Drain()
	if len(events) != 1 || events[0].Category != "ssh_failed" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestAuthClosedDetected(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Connection closed by authenticating user carol 2.2.2.2 port 50000 [preauth]",
		time.Now())
	events := m.Drain()
	if len(events) != 1 || events[0].Category != "ssh_failed" || events[0].IP != "2.2.2.2" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestSudoDetected(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sudo: alice : TTY=pts/0 ; PWD=/home/alice ; USER=root ; COMMAND=/usr/bin/apt install nginx",
		time.Now())
	events := m.Drain()
	if len(events) != 1 || events[0].Category != "sudo" || events[0].Actor != "alice" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestDpkgInstallDetected(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/dpkg.log",
		"2026-09-27 08:00:42 install nginx:amd64 1.2.3 <none>",
		time.Now())
	events := m.Drain()
	if len(events) != 1 || events[0].Category != "install" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestAptHistoryDetected(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/apt/history.log",
		"Install: nginx:amd64 (1.2.3), curl:amd64 (4.5.6)",
		time.Now())
	events := m.Drain()
	if len(events) != 1 || events[0].Category != "install" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestNotifyMinLevelFiltersInfo(t *testing.T) {
	m := newTestMonitor("warning")
	m.loginKnownIPs["1.2.3.4"] = true
	// info の SSH ログインはキューに積まれない。
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Accepted password for alice from 1.2.3.4 port 50000 ssh2",
		time.Now())
	events := m.Drain()
	if len(events) != 0 {
		t.Fatalf("info should be filtered out, got %+v", events)
	}
}

// ---- SSH 失敗ログインのエスカレーション（2026-10-04 / 攻撃 A-4 の回帰） ----

// 最初の失敗が窓より古い状態で短時間に集中した失敗は、旧実装（最初の失敗を
// 基準にした固定窓）では数えられなかった。スライディング窓では検出する。
func TestSSHFailedSlidingWindowEscalates(t *testing.T) {
	m := newTestMonitor("warning")
	m.cfg.FailedBurst = 5
	m.cfg.BurstWindow = 60

	now := time.Now()
	// 0秒に1回試した後、55秒から4秒間隔で5回（55/59/63/67/71）。
	// 旧実装は63秒時点で窓を切り直すため以降は数え直しになり critical に
	// 到達しない。スライディング窓は直近60秒で5回を数える。
	offsets := []int{0, 55, 59, 63, 67, 71}
	for _, off := range offsets {
		m.classify("/var/log/auth.log",
			"Sep 27 08:00:42 server1 sshd[1]: Failed password for bob from 9.9.9.9 port 50000 ssh2",
			now.Add(time.Duration(off)*time.Second))
	}
	events := m.Drain()
	if len(events) != len(offsets) {
		t.Fatalf("events = %d, want %d", len(events), len(offsets))
	}
	if last := events[len(events)-1]; last.Level != "critical" {
		t.Errorf("last level = %s, want critical (sliding window)", last.Level)
	}
}

// 短窓の burst を避けた低頻度（40秒間隔）の試行でも、長窓の総数で critical に
// 到達する。
func TestSSHFailedSustainedLowAndSlowEscalates(t *testing.T) {
	m := newTestMonitor("warning")
	m.cfg.FailedBurst = 5
	m.cfg.BurstWindow = 60
	m.cfg.SSHFailedSustainedBurst = 15
	m.cfg.SSHFailedSustainedWindow = 600

	now := time.Now()
	for i := 0; i < 15; i++ {
		m.classify("/var/log/auth.log",
			"Sep 27 08:00:42 server1 sshd[1]: Failed password for bob from 9.9.9.9 port 50000 ssh2",
			now.Add(time.Duration(i*40)*time.Second))
	}
	events := m.Drain()
	if len(events) != 15 {
		t.Fatalf("events = %d, want 15", len(events))
	}
	if events[13].Level != "warning" {
		t.Errorf("14th level = %s, want warning", events[13].Level)
	}
	if events[14].Level != "critical" {
		t.Fatalf("15th level = %s, want critical (sustained)", events[14].Level)
	}
	if want := msg("ja", "ssh_failed.sustained.title"); events[14].Title != want {
		t.Errorf("title = %q, want %q", events[14].Title, want)
	}
}

// ユーザー名を回す攻撃は1回あたりの頻度が低く burst では捉えられない。
// 種類の多さ（列挙）で critical にする。攻撃 A-4 はこの形だった。
func TestSSHFailedUsernameEnumerationEscalates(t *testing.T) {
	m := newTestMonitor("warning") // FailedBurst=5/60秒, 列挙=5種類/300秒
	now := time.Now()
	for i := 0; i < 5; i++ {
		line := "Sep 27 08:00:42 server1 sshd[1]: Invalid user kizuna_nouser_" + itoa(i) + " from 127.0.0.1 port 50000"
		m.classify("/var/log/auth.log", line, now.Add(time.Duration(i*30)*time.Second))
	}
	events := m.Drain()
	if len(events) != 5 {
		t.Fatalf("events = %d, want 5", len(events))
	}
	for i := 0; i < 4; i++ {
		if events[i].Level != "warning" {
			t.Errorf("event %d level = %s, want warning", i, events[i].Level)
		}
	}
	if events[4].Level != "critical" {
		t.Fatalf("5th level = %s, want critical (enumeration)", events[4].Level)
	}
	if want := msg("ja", "ssh_failed.enum.title"); events[4].Title != want {
		t.Errorf("title = %q, want %q", events[4].Title, want)
	}
	if !strings.Contains(events[4].Message, "kizuna_nouser_0") {
		t.Errorf("message should list the attempted usernames: %q", events[4].Message)
	}
	if !strings.Contains(events[4].Message, "ループバック") {
		t.Errorf("message should note the loopback source: %q", events[4].Message)
	}
}

// ループバック以外の失敗にはループバックの注記を付けない。
func TestSSHFailedLoopbackNoteOnlyForLoopback(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Failed password for bob from 203.0.113.9 port 50000 ssh2", time.Now())
	events := m.Drain()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if strings.Contains(events[0].Message, "ループバック") {
		t.Errorf("non-loopback message must not note loopback: %q", events[0].Message)
	}

	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Failed password for bob from 127.0.0.1 port 50000 ssh2", time.Now())
	events = m.Drain()
	if len(events) != 1 || !strings.Contains(events[0].Message, "ループバック") {
		t.Fatalf("loopback message should note loopback: %+v", events)
	}
}

// 同一IPの「ログイン成功の急増」は、冷却時間内かつ倍増していない限り再通知しない
// （監視ツールの反復接続で通知が埋まるのを防ぐ / 監査 F-6）。
func TestSSHLoginBurstReAlertIsRateLimited(t *testing.T) {
	m := newTestMonitor("warning")
	m.cfg.SSHLoginBurst = 10
	m.cfg.SSHLoginWindow = 300
	m.loginKnownIPs["1.2.3.4"] = true

	now := time.Now()
	loginAt := func(off int) {
		m.classify("/var/log/auth.log",
			"Sep 27 08:00:42 server1 sshd[1]: Accepted password for alice from 1.2.3.4 port 50000 ssh2",
			now.Add(time.Duration(off)*time.Second))
	}
	criticals := func() int {
		n := 0
		for _, ev := range m.Drain() {
			if ev.Level == "critical" {
				n++
			}
		}
		return n
	}

	// 1) 最初の急増（窓内10回）で critical 1件。
	for i := 0; i < 10; i++ {
		loginAt(i)
	}
	if got := criticals(); got != 1 {
		t.Fatalf("first burst criticals = %d, want 1", got)
	}
	// 2) 冷却時間（300秒）内に倍増（30回）しても再通知しない。
	for i := 10; i < 30; i++ {
		loginAt(i)
	}
	if got := criticals(); got != 0 {
		t.Errorf("re-alert during cooldown = %d, want 0", got)
	}
	// 3) 冷却後でも、倍増していなければ再通知しない（窓が変わって10回）。
	for i := 310; i < 320; i++ {
		loginAt(i)
	}
	if got := criticals(); got != 0 {
		t.Errorf("re-alert without doubling = %d, want 0", got)
	}
	// 4) 冷却後に倍増（窓内20回）すれば再通知する。
	for i := 320; i < 330; i++ {
		loginAt(i)
	}
	if got := criticals(); got != 1 {
		t.Errorf("re-alert after cooldown and doubling = %d, want 1", got)
	}
}

// 設定の受け取りとクランプ。
func TestParseConfigSSHFailureTuning(t *testing.T) {
	cfg, err := ParseConfig(map[string]interface{}{
		"watch_files":                 "/var/log/auth.log",
		"failed_sustained_burst":      float64(20),
		"failed_sustained_window_sec": float64(900),
		"ssh_enum_distinct_users":     float64(3),
		"ssh_enum_window_sec":         float64(120),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.SSHFailedSustainedBurst != 20 || cfg.SSHFailedSustainedWindow != 900 ||
		cfg.SSHEnumDistinctUsers != 3 || cfg.SSHEnumWindow != 120 {
		t.Fatalf("unexpected tuning: %+v", cfg)
	}

	// 長窓は短窓以上へ、列挙の種類数は 2 以上へ、窓は 5 秒以上へクランプされる。
	cfg2, err := ParseConfig(map[string]interface{}{
		"watch_files":                 "/var/log/auth.log",
		"burst_window_sec":            float64(120),
		"failed_sustained_window_sec": float64(30),
		"ssh_enum_distinct_users":     float64(1),
		"ssh_enum_window_sec":         float64(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg2.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg2.SSHFailedSustainedWindow < cfg2.BurstWindow {
		t.Errorf("sustained window = %d, want >= burst window %d", cfg2.SSHFailedSustainedWindow, cfg2.BurstWindow)
	}
	if cfg2.SSHEnumDistinctUsers != 5 {
		t.Errorf("enum distinct = %d, want 5 (clamped)", cfg2.SSHEnumDistinctUsers)
	}
	if cfg2.SSHEnumWindow != 300 {
		t.Errorf("enum window = %d, want 300 (clamped)", cfg2.SSHEnumWindow)
	}
}
