package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// ---- SSH login-success anomaly ----

func TestSSHLoginUnknownIPWarns(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Accepted password for alice from 9.9.9.9 port 50000 ssh2",
		time.Now())
	events := m.Drain()
	found := false
	for _, ev := range events {
		if ev.Category == "ssh_login" && ev.Level == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown IP login should emit a warning: %+v", events)
	}
}

func TestSSHLoginKnownIPNoWarn(t *testing.T) {
	m := newTestMonitor("warning")
	m.loginKnownIPs["9.9.9.9"] = true
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Accepted password for alice from 9.9.9.9 port 50000 ssh2",
		time.Now())
	for _, ev := range m.Drain() {
		if ev.Level == "warning" {
			t.Fatalf("known IP should not warn: %+v", ev)
		}
	}
}

func TestSSHLoginBurstCritical(t *testing.T) {
	m := newTestMonitor("warning")
	m.cfg.SSHLoginBurst = 3
	m.cfg.SSHLoginWindow = 300
	m.loginKnownIPs["9.9.9.9"] = true

	now := time.Now()
	for i := 0; i < 3; i++ {
		m.classify("/var/log/auth.log",
			"Sep 27 08:00:42 server1 sshd[1]: Accepted password for alice from 9.9.9.9 port 50000 ssh2",
			now.Add(time.Duration(i)*time.Second))
	}
	critical := 0
	for _, ev := range m.Drain() {
		if ev.Category == "ssh_login" && ev.Level == "critical" {
			critical++
		}
	}
	if critical != 1 {
		t.Fatalf("critical login burst count = %d, want 1", critical)
	}
}

func TestLoginStatePersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logins.json")

	cfg := DefaultConfig()
	cfg.SSHLoginBaselinePath = path
	m := NewMonitor(cfg, nil, nil)
	m.loginKnownIPs["9.9.9.9"] = true
	m.saveLoginState()

	m2 := NewMonitor(cfg, nil, nil)
	if !m2.loginKnownIPs["9.9.9.9"] {
		t.Fatal("known IP should persist across restarts")
	}
}

// ---- yum / dnf ----

func TestYumInstallDetected(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/yum.log",
		"Oct 02 10:00:00 Installed: nginx-1.24.0-1.el9.x86_64",
		time.Now())
	events := m.Drain()
	if len(events) != 1 || events[0].Category != "install" {
		t.Fatalf("yum install should be detected: %+v", events)
	}
}

func TestDnfUpdatedDetected(t *testing.T) {
	m := newTestMonitor("warning")
	m.classify("/var/log/dnf.log",
		"2026-10-02T10:00:00Z DEBUG Updated: openssl-3.0.7-1.el9.x86_64",
		time.Now())
	events := m.Drain()
	if len(events) != 1 || events[0].Category != "install" {
		t.Fatalf("dnf update should be detected: %+v", events)
	}
}

// ---- validateWatchPath accepts RHEL logs ----

func TestValidateWatchPathRHEL(t *testing.T) {
	for _, p := range []string{"/var/log/secure", "/var/log/messages", "/var/log/yum.log"} {
		if err := validateWatchPath(p); err != nil {
			t.Errorf("validateWatchPath(%q) should pass: %v", p, err)
		}
	}
}

// ---- cron ----

func TestCronDetectsChange(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "crontab")
	baseline := filepath.Join(dir, "cron.json")

	if err := os.WriteFile(target, []byte("# empty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }

	cm := NewCronMonitor([]string{target}, baseline, "ja", nil, emit)
	cm.Check() // baseline
	if len(events) != 0 {
		t.Fatalf("baseline should be silent: %+v", events)
	}

	if err := os.WriteFile(target, []byte("* * * * * root /tmp/evil.sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cm.Check()
	found := false
	for _, ev := range events {
		if ev.Category == "cron" && ev.Level == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cron change should be critical: %+v", events)
	}
}

// ---- config parsing for the new options ----

func TestParseNewConfigOptions(t *testing.T) {
	raw := map[string]interface{}{
		"listen_port_check":      "true",
		"suid_check":             false,
		"cron_check":             "false",
		"ssh_login_burst":        float64(7),
		"firewall_backend":       "nftables",
		"suid_scan_interval_sec": float64(120),
	}
	c, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !c.ListenPortCheck {
		t.Error("listen_port_check should be true")
	}
	if c.SUIDCheck {
		t.Error("suid_check should be false")
	}
	if c.CronCheck {
		t.Error("cron_check should be false")
	}
	if c.SSHLoginBurst != 7 {
		t.Errorf("ssh_login_burst = %d, want 7", c.SSHLoginBurst)
	}
	if c.FirewallBackend != "nftables" {
		t.Errorf("firewall_backend = %s, want nftables", c.FirewallBackend)
	}
	if c.SUIDScanInterval != 120 {
		t.Errorf("suid_scan_interval_sec = %d, want 120", c.SUIDScanInterval)
	}
}
