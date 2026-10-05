package main

import (
	"testing"
	"time"
)

// TestSpoofedLoginDoesNotTriggerBurst is the regression test for the attack
// test finding: 51 forged auth.log lines (logger -t sshd) were counted as
// ssh_login INFO and one of them (fakeuser) escalated to a critical ssh_login
// burst. A forged line must be recorded as spoofed_log and must NOT enter the
// ssh_login counter.
func TestSpoofedLoginDoesNotTriggerBurst(t *testing.T) {
	m := newTestMonitor("info")
	// The journald watcher marks the exact body of a forged line.
	forged := "Accepted password for fakeuser from 1.2.3.4 port 22 ssh2"
	m.spoof.add(forged, time.Now())

	// Feed the forged line 10 times. Before the fix each one incremented the
	// ssh_login counter and could fire a burst at the threshold.
	line := "Oct  4 12:00:00 server1 sshd[1]: " + forged
	for i := 0; i < 10; i++ {
		m.classify("/var/log/auth.log", line, time.Now())
	}

	events := m.Drain()
	var spoofed, logins int
	for _, ev := range events {
		switch ev.Category {
		case "spoofed_log":
			spoofed++
		case "ssh_login":
			logins++
		}
	}
	if spoofed != 10 {
		t.Errorf("spoofed_log events = %d, want 10", spoofed)
	}
	if logins != 0 {
		t.Errorf("ssh_login events = %d, want 0 (forged line must not be counted)", logins)
	}
}

// TestRealLoginStillCounts verifies a non-forged line is processed normally
// (the spoof check must not swallow legitimate logins).
func TestRealLoginStillCounts(t *testing.T) {
	m := newTestMonitor("info")
	m.loginKnownIPs["10.0.0.5"] = true // suppress the "unknown IP" warning

	line := "Oct  4 12:00:00 server1 sshd[2]: Accepted password for realuser from 10.0.0.5 port 22 ssh2"
	m.classify("/var/log/auth.log", line, time.Now())

	events := m.Drain()
	var logins int
	for _, ev := range events {
		if ev.Category == "ssh_login" {
			logins++
		}
	}
	if logins != 1 {
		t.Fatalf("ssh_login events = %d, want 1 (real login must be recorded)", logins)
	}
}

// TestSpoofedLineNotCountedAsFailure verifies a forged FAILED line does not
// feed the ssh_failed burst counter either.
func TestSpoofedLineNotCountedAsFailure(t *testing.T) {
	m := newTestMonitor("info")
	forged := "Failed password for fakeuser from 1.2.3.4 port 22 ssh2"
	m.spoof.add(forged, time.Now())

	line := "Oct  4 12:00:00 server1 sshd[3]: " + forged
	for i := 0; i < 10; i++ {
		m.classify("/var/log/auth.log", line, time.Now())
	}

	events := m.Drain()
	for _, ev := range events {
		if ev.Category == "ssh_failed" {
			t.Errorf("forged FAILED line must not be counted as ssh_failed: %+v", ev)
		}
	}
}
