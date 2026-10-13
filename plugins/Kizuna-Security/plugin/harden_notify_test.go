package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHostLogger implements module.Logger and records everything the plugin
// reports through the host. It matters at startup: the plugin's own event
// channel (Monitor) does not exist yet when the chain key fails to load, so
// the host log and the DISABLED marker are the only notification paths.
type fakeHostLogger struct{ msgs []string }

func (f *fakeHostLogger) Debug(format string, args ...interface{}) {
	f.msgs = append(f.msgs, "DEBUG "+fmt.Sprintf(format, args...))
}

func (f *fakeHostLogger) Info(format string, args ...interface{}) {
	f.msgs = append(f.msgs, "INFO "+fmt.Sprintf(format, args...))
}

func (f *fakeHostLogger) Warn(format string, args ...interface{}) {
	f.msgs = append(f.msgs, "WARN "+fmt.Sprintf(format, args...))
}

func (f *fakeHostLogger) Error(format string, args ...interface{}) {
	f.msgs = append(f.msgs, "ERROR "+fmt.Sprintf(format, args...))
}

func (f *fakeHostLogger) contains(sub string) bool {
	for _, m := range f.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// testPluginConfig builds a minimal valid config rooted in dir, with the
// optional monitors that shell out (ports/suid/cron) disabled.
func testPluginConfig(t *testing.T, dir string) map[string]interface{} {
	t.Helper()
	return map[string]interface{}{
		"watch_files":             filepath.Join(dir, "auth.log"),
		"log_path":                filepath.Join(dir, "kizuna-security.log"),
		"chain_key_path":          filepath.Join(dir, "chain.key"),
		"integrity_baseline_path": filepath.Join(dir, "kizuna-security-fim.json"),
		"poll_interval_sec":       15.0,
		"notify_min_level":        "warning",
		"language":                "en",
		"listen_port_check":       false,
		"suid_check":              false,
		"cron_check":              false,
	}
}

// TestChainQuarantineIsReported pins High-5: an existing log that cannot be
// verified any more (tampering, or a rotated key) is archived, and the fact
// must be observable — previously the process silently started a new chain.
func TestChainQuarantineIsReported(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kizuna-security.log")
	keyPath := filepath.Join(dir, "chain.key")

	if _, err := loadOrCreateChainKey(keyPath); err != nil {
		t.Fatalf("loadOrCreateChainKey: %v", err)
	}
	flA, err := NewFileLoggerKeyed(logPath, keyPath)
	if err != nil {
		t.Fatalf("NewFileLoggerKeyed: %v", err)
	}
	flA.Info("test", "first entry", nil)
	if err := flA.Close(); err != nil {
		t.Fatal(err)
	}

	// Rotate the key: the existing log can no longer be verified.
	if err := os.WriteFile(keyPath, []byte("rotated-key-material-32-bytes!!"), 0600); err != nil {
		t.Fatal(err)
	}
	flB, err := NewFileLoggerKeyed(logPath, keyPath)
	if err != nil {
		t.Fatalf("NewFileLoggerKeyed after rotation: %v", err)
	}
	defer flB.Close()

	archive, reason := flB.Quarantine()
	if archive == "" || reason == "" {
		t.Fatal("quarantine must be reported, not silent (High-5)")
	}
	if _, err := os.Stat(archive); err != nil {
		t.Errorf("archived log missing: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "chain_quarantined") {
		t.Errorf("the new log must record the quarantine, got: %s", raw)
	}
}

// TestConfigureEmitsCriticalOnQuarantine checks the notification path: a
// quarantine has to reach the dashboard/Discord channel as a critical event.
func TestConfigureEmitsCriticalOnQuarantine(t *testing.T) {
	dir := t.TempDir()
	raw := testPluginConfig(t, dir)
	logPath := raw["log_path"].(string)
	keyPath := raw["chain_key_path"].(string)

	if _, err := loadOrCreateChainKey(keyPath); err != nil {
		t.Fatal(err)
	}
	fl, err := NewFileLoggerKeyed(logPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	fl.Info("test", "first entry", nil)
	fl.Close()
	if err := os.WriteFile(keyPath, []byte("rotated-key-material-32-bytes!!"), 0600); err != nil {
		t.Fatal(err)
	}

	lg := &fakeHostLogger{}
	p := NewPluginModule(lg).(*SecurityPlugin)
	if err := p.Configure(raw); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	defer func() { _ = p.Stop() }()

	var quarantines int
	for _, ev := range p.monitor.Drain() {
		if ev.Source != "log_chain_quarantined" {
			continue
		}
		quarantines++
		if ev.Level != "critical" {
			t.Errorf("level = %s, want critical", ev.Level)
		}
		if ev.Message == "" || strings.Contains(ev.Message, "%!") {
			t.Errorf("message not formatted: %q", ev.Message)
		}
	}
	if quarantines != 1 {
		t.Fatalf("critical quarantine events = %d, want 1", quarantines)
	}
}

// TestConfigureFailsLoudlyWhenChainKeyUnreadable pins Medium-7: without the
// key the plugin cannot run, and that must not look like "no module".
func TestConfigureFailsLoudlyWhenChainKeyUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits")
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "chain.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0000); err != nil {
		t.Fatal(err)
	}

	lg := &fakeHostLogger{}
	p := NewPluginModule(lg).(*SecurityPlugin)
	if err := p.Configure(testPluginConfig(t, dir)); err == nil {
		t.Fatal("Configure must fail closed when the chain key is unreadable")
	}
	if !lg.contains("Kizuna-Security を開始できません") {
		t.Errorf("host log must explain the shutdown: %v", lg.msgs)
	}

	marker := filepath.Join(dir, "kizuna-security-DISABLED.json")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("disabled marker missing (Medium-7): %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("marker is not JSON: %v", err)
	}
	if m["reason"] == nil || m["reason"] == "" {
		t.Errorf("marker must carry the reason: %v", m)
	}
	if m["chain_key_path"] != keyPath {
		t.Errorf("marker chain_key_path = %v, want %s", m["chain_key_path"], keyPath)
	}
}

// TestChainKeyPermissionWarningRepeats pins Medium-8: the old sync.Once warned
// at most once per process, so a regression after the first warning was never
// reported again.
func TestChainKeyPermissionWarningRepeats(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits")
	}
	oldInterval := chainKeyPermWarnInterval
	oldWarner := chainKeyPermWarner
	chainKeyPermWarnInterval = 20 * time.Millisecond
	chainKeyPermWarner = &permWarnState{lastPerm: map[string]string{}, lastAt: map[string]time.Time{}}
	defer func() {
		chainKeyPermWarnInterval = oldInterval
		chainKeyPermWarner = oldWarner
	}()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "chain.key")
	if err := os.WriteFile(keyPath, []byte("k"), 0644); err != nil {
		t.Fatal(err)
	}

	p := &SecurityPlugin{}
	mon := newTestMonitor("warning")
	total := 0
	count := func() int { total += len(mon.Drain()); return total }

	warnChainKeyPermissions(p, mon, keyPath, "en")
	if got := count(); got != 1 {
		t.Fatalf("first warning = %d, want 1", got)
	}
	warnChainKeyPermissions(p, mon, keyPath, "en")
	if got := count(); got != 1 {
		t.Fatalf("duplicate warning inside the interval (%d)", got)
	}
	time.Sleep(3 * chainKeyPermWarnInterval)
	warnChainKeyPermissions(p, mon, keyPath, "en")
	if got := count(); got != 2 {
		t.Fatalf("warning must repeat after the interval (%d)", got)
	}

	// Fixing the mode clears the record, so a regression warns immediately.
	if err := os.Chmod(keyPath, 0600); err != nil {
		t.Fatal(err)
	}
	warnChainKeyPermissions(p, mon, keyPath, "en")
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	warnChainKeyPermissions(p, mon, keyPath, "en")
	if got := count(); got != 3 {
		t.Fatalf("regression after a fix must warn immediately (%d)", got)
	}
}

// TestQuarantinedLogIsTightened pins Low-14: the archived log inherits the
// mode of the original file, so a log created before the 0600 tightening
// would stay group/other readable even though it holds usernames and IPs.
func TestQuarantinedLogIsTightened(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kizuna-security.log")
	keyPath := filepath.Join(dir, "chain.key")

	if _, err := loadOrCreateChainKey(keyPath); err != nil {
		t.Fatalf("loadOrCreateChainKey: %v", err)
	}
	flA, err := NewFileLoggerKeyed(logPath, keyPath)
	if err != nil {
		t.Fatalf("NewFileLoggerKeyed: %v", err)
	}
	flA.Info("test", "first entry", nil)
	if err := flA.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a log written by an older version that did not tighten modes.
	if err := os.Chmod(logPath, 0644); err != nil {
		t.Fatal(err)
	}
	// Rotate the key: the existing log can no longer be verified.
	if err := os.WriteFile(keyPath, []byte("rotated-key-material-32-bytes!!"), 0600); err != nil {
		t.Fatal(err)
	}

	flB, err := NewFileLoggerKeyed(logPath, keyPath)
	if err != nil {
		t.Fatalf("NewFileLoggerKeyed after rotation: %v", err)
	}
	defer flB.Close()

	archive, _ := flB.Quarantine()
	if archive == "" {
		t.Fatal("expected the unverifiable log to be archived")
	}
	fi, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("archive perm = %04o, want 0600 (Low-14)", perm)
	}
}
