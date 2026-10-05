package main

import (
	"testing"
	"time"
)

// TestSSHLoginAllowlistSuppresses: a login from an allowlisted IP is not
// recorded or counted, so a monitoring host's reconnects do not fire the
// "login burst" critical (task 7).
func TestSSHLoginAllowlistSuppresses(t *testing.T) {
	m := newTestMonitor("info")
	m.cfg.SSHLoginBurst = 3
	m.cfg.SSHLoginWindow = 300
	m.cfg.SSHLoginAllowlist = []string{"192.168.0.139"}

	for i := 0; i < 10; i++ {
		m.classify("/var/log/auth.log",
			"Oct  4 12:00:00 server1 sshd[1]: Accepted password for ops from 192.168.0.139 port 22 ssh2",
			time.Now())
	}
	events := m.Drain()
	for _, ev := range events {
		if ev.Category == "ssh_login" {
			t.Errorf("allowlisted IP must not produce ssh_login events: %+v", ev)
		}
	}
}

// TestSSHLoginAllowlistDoesNotAffectOthers: a non-allowlisted IP still counts
// and can escalate to the burst critical.
func TestSSHLoginAllowlistDoesNotAffectOthers(t *testing.T) {
	m := newTestMonitor("info")
	m.cfg.SSHLoginBurst = 3
	m.cfg.SSHLoginWindow = 300
	m.cfg.SSHLoginAllowlist = []string{"192.168.0.139"}

	for i := 0; i < 5; i++ {
		m.classify("/var/log/auth.log",
			"Oct  4 12:00:00 server1 sshd[1]: Accepted password for bob from 10.0.0.5 port 22 ssh2",
			time.Now())
	}
	events := m.Drain()
	var logins, bursts int
	for _, ev := range events {
		switch ev.Category {
		case "ssh_login":
			if ev.Level == "critical" {
				bursts++
			} else {
				logins++
			}
		}
	}
	if logins == 0 {
		t.Errorf("non-allowlisted IP must still produce ssh_login events")
	}
	if bursts != 1 {
		t.Errorf("non-allowlisted IP burst = %d, want 1", bursts)
	}
}
