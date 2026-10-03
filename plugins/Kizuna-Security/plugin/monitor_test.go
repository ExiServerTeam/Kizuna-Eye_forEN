package main

import (
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
