package main

import (
	"os"
	"path/filepath"
	"testing"

	"Kizuna-Eye/pkg/module"
)

// ハッシュチェーンが正しく検証でき、改ざんを検出できることを確認する。
func TestLoggerHashChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sec.log")

	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("test", "one", nil)
	l.Warn("test", "two", nil)
	l.Error("test", "three", nil)
	_ = l.Close()

	if err := VerifyChain(path); err != nil {
		t.Fatalf("intact chain should verify: %v", err)
	}

	// 改ざん: 1行目を書き換える。
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitLines(string(data))
	if len(lines) < 2 {
		t.Fatalf("expected >=2 lines, got %d", len(lines))
	}
	lines[0] = `{"ts":"x","level":"INFO","event":"tampered","message":"x","prev_hash":"","hash":"deadbeef"}`
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0600); err != nil {
		t.Fatal(err)
	}

	if err := VerifyChain(path); err == nil {
		t.Fatal("tampered chain should fail verification")
	}
}

// 再起動後もチェーンが継続することを確認する。
func TestLoggerChainContinuesAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sec.log")

	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Info("test", "one", nil)
	_ = l.Close()

	l2, err := NewFileLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	l2.Info("test", "two", nil)
	_ = l2.Close()

	if err := VerifyChain(path); err != nil {
		t.Fatalf("chain across reopen should verify: %v", err)
	}
}

// FIM: 初回はイベントなし、変更で critical、削除で warning を検知する。
func TestFIMDetectsChangeAndDelete(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "passwd")
	baseline := filepath.Join(dir, "fim.json")

	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}

	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }

	fim := NewFIM([]string{target}, baseline, nil, emit)

	// 初回: イベントなし。
	fim.Check()
	if len(events) != 0 {
		t.Fatalf("first run should emit nothing, got %d", len(events))
	}

	// 変更: critical を検知。
	if err := os.WriteFile(target, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	fim.Check()
	if len(events) != 1 || events[0].Level != "critical" || events[0].Category != "integrity" {
		t.Fatalf("change should be critical integrity event: %+v", events)
	}

	// 削除: warning を検知。
	events = nil
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	fim.Check()
	if len(events) != 1 || events[0].Level != "warning" || events[0].Category != "integrity" {
		t.Fatalf("delete should be warning integrity event: %+v", events)
	}
}

// FIM: 新規作成を critical として検知する。
func TestFIMDetectsCreate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "authorized_keys")
	baseline := filepath.Join(dir, "fim.json")

	// 事前に空ファイルでベースラインを取る（存在しないと記録されないため）。
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }
	fim := NewFIM([]string{target}, baseline, nil, emit)
	fim.Check() // baseline

	// 削除してから再作成 → 「作成」と「削除」を検知する。
	events = nil
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	fim.Check()

	events = nil
	if err := os.WriteFile(target, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	fim.Check()

	found := false
	for _, ev := range events {
		if ev.Level == "critical" && ev.Category == "integrity" {
			found = true
		}
	}
	if !found {
		t.Fatalf("create should be critical integrity event: %+v", events)
	}
}

// 監視対象に新規追加されたファイルは、誤警報を出さずベースライン化される。
func TestFIMNewWatchTargetSilent(t *testing.T) {
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

	// まず a のみ監視。
	fim := NewFIM([]string{fileA}, baseline, nil, emit)
	fim.Check() // baseline for a
	if len(events) != 0 {
		t.Fatalf("initial baseline should be silent: %+v", events)
	}

	// b を監視対象に追加。新規追加は黙ってベースライン化されるべき。
	events = nil
	fim2 := NewFIM([]string{fileA, fileB}, baseline, nil, emit)
	fim2.Check()
	// A newly added watch target is reported (level=warning) on purpose:
	// the operator must be able to confirm the addition, and an attacker
	// must not be able to slip a file into the baseline unnoticed. This is
	// NOT a critical change alert.
	if len(events) != 1 || events[0].Level != "warning" || events[0].Category != "integrity" {
		t.Fatalf("newly added watch target should emit one integrity warning, got: %+v", events)
	}

	// その後 b を改ざんすると critical を検知する。
	events = nil
	if err := os.WriteFile(fileB, []byte("b-changed"), 0600); err != nil {
		t.Fatal(err)
	}
	fim2.Check()
	if len(events) != 1 || events[0].Level != "critical" {
		t.Fatalf("change of watched file should be critical: %+v", events)
	}
}

// splitLines / joinLines helpers for the test.
func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func joinLines(lines []string) string {
	s := ""
	for _, l := range lines {
		s += l + "\n"
	}
	return s
}
