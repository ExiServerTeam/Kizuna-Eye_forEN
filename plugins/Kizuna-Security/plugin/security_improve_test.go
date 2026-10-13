package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// ---- i18n ----

func TestMessagesLocalized(t *testing.T) {
	if got := msg("ja", "ssh_failed.burst.title"); got != "SSH ログイン失敗の多発" {
		t.Errorf("ja title = %q", got)
	}
	if got := msg("en", "ssh_failed.burst.title"); got != "Repeated SSH login failures" {
		t.Errorf("en title = %q", got)
	}
	// Unknown language falls back to Japanese.
	if got := msg("fr", "ssh_failed.burst.title"); got != "SSH ログイン失敗の多発" {
		t.Errorf("fallback title = %q", got)
	}
	// Formatting args are applied.
	if got := msg("en", "ssh_failed.burst.msg", "1.2.3.4", 7); !strings.Contains(got, "1.2.3.4") || !strings.Contains(got, "7") {
		t.Errorf("en burst msg = %q", got)
	}
}

func TestMonitorEmitsEnglish(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Language = "en"
	cfg.NotifyMinimal = "warning"
	m := NewMonitor(cfg, nil, nil)
	m.classify("/var/log/auth.log",
		"Sep 27 08:00:42 server1 sshd[1]: Failed password for bob from 5.6.7.8 port 50000 ssh2",
		time.Now())
	events := m.Drain()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Title != "SSH login failed" {
		t.Errorf("title = %q, want English", events[0].Title)
	}
}

// ---- SUID scan window ----

func TestSUIDScanHourSkipsOtherHours(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}

	baseline := filepath.Join(dir, "suid.json")
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }

	// scanHour = the hour after the current one so the scan is skipped.
	other := (time.Now().Hour() + 1) % 24
	sm := NewSUIDMonitor([]string{dir}, baseline, time.Hour, other, "ja", nil, emit)
	sm.Check()
	if len(events) != 0 {
		t.Fatalf("scan outside the window should do nothing: %+v", events)
	}
}

func TestSUIDDetectsNewFile(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "suid.json")
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }

	sm := NewSUIDMonitor([]string{dir}, baseline, time.Second, -1, "ja", nil, emit)
	sm.Check() // baseline (no SUID files yet)
	if len(events) != 0 {
		t.Fatalf("baseline should be silent: %+v", events)
	}

	if err := os.Chmod(bin, 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	sm.lastScan = time.Time{} // force a rescan
	sm.Check()

	found := false
	for _, ev := range events {
		if ev.Category == "suid" && ev.Level == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatalf("new SUID file should be critical: %+v", events)
	}
}

// ---- Block state persistence ----

func TestBlockerStateRestored(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "blocks.json")

	// Persist a block that is still in the future.
	until := time.Now().Add(5 * time.Minute).UTC()
	st := blockState{Blocks: map[string]time.Time{"203.0.113.9": until}}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(state, data, 0600); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.BlockMode = "enforce"
	cfg.BlockStatePath = state
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }

	b := newBlocker(cfg, nil, emit)
	if _, ok := b.blocked["203.0.113.9"]; !ok {
		t.Fatal("block should be restored from state")
	}
	if len(events) != 1 || events[0].Category != "block" {
		t.Fatalf("restore should emit one block event: %+v", events)
	}
}

func TestBlockerStateDropsExpired(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "blocks.json")

	st := blockState{Blocks: map[string]time.Time{"203.0.113.9": time.Now().Add(-time.Minute).UTC()}}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(state, data, 0600); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.BlockMode = "enforce"
	cfg.BlockStatePath = state
	b := newBlocker(cfg, nil, nil)
	if len(b.blocked) != 0 {
		t.Fatalf("expired block should be dropped: %+v", b.blocked)
	}
}

// ---- FIM baseline notice ----

func TestFIMNotifiesNewWatchTarget(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.conf")
	fileB := filepath.Join(dir, "b.conf")
	baseline := filepath.Join(dir, "fim.json")

	if err := os.WriteFile(fileA, []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("b"), 0600); err != nil {
		t.Fatal(err)
	}

	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }

	fim := NewFIM([]string{fileA}, baseline, nil, emit)
	fim.Check()

	// Add fileB as a new watch target: it must now be reported, not silent.
	events = nil
	fim2 := NewFIM([]string{fileA, fileB}, baseline, nil, emit)
	fim2.Check()
	if len(events) != 1 {
		t.Fatalf("new watch target should emit one notice, got %+v", events)
	}
	if events[0].Title == "" || events[0].Level != "warning" {
		t.Fatalf("unexpected baseline event: %+v", events[0])
	}
}

// ---- VerifyChain: tamper / delete / insert ----

func TestVerifyChainDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sec.log")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("a", "one", nil)
	l.Info("b", "two", nil)
	l.Info("c", "three", nil)
	_ = l.Close()

	lines := readLines(t, path)
	lines[1] = strings.Replace(lines[1], "two", "TAMPERED", 1)
	writeLines(t, path, lines)

	if err := VerifyChain(path); err == nil {
		t.Fatal("tampered chain must fail")
	}
}

func TestVerifyChainDetectsDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sec.log")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("a", "one", nil)
	l.Info("b", "two", nil)
	l.Info("c", "three", nil)
	_ = l.Close()

	lines := readLines(t, path)
	// Drop the middle line: prev_hash of the last line no longer matches.
	writeLines(t, path, append(lines[:1], lines[2:]...))

	if err := VerifyChain(path); err == nil {
		t.Fatal("deleted line must fail verification")
	}
}

func TestVerifyChainDetectsInsert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sec.log")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("a", "one", nil)
	l.Info("b", "two", nil)
	_ = l.Close()

	lines := readLines(t, path)
	// Insert a fabricated line in the middle.
	fake := `{"ts":"x","level":"INFO","event":"evil","message":"x","prev_hash":"","hash":"deadbeef"}`
	lines = append(lines[:1], append([]string{fake}, lines[1:]...)...)
	writeLines(t, path, lines)

	if err := VerifyChain(path); err == nil {
		t.Fatal("inserted line must fail verification")
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
