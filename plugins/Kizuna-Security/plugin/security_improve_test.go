package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// ---- i18n ----

// ja/en のキー集合が一致すること。片方だけ追加すると、もう片方の言語で
// 日本語へフォールバックして気付きにくい（今回の ssh_failed.* 追加で実際に
// 起こり得た）。
func TestMessageCatalogParity(t *testing.T) {
	ja, en := catalog["ja"], catalog["en"]
	if len(ja) == 0 || len(en) == 0 {
		t.Fatal("message catalog is empty")
	}
	for k := range ja {
		if _, ok := en[k]; !ok {
			t.Errorf("key %q exists in ja but not in en", k)
		}
	}
	for k := range en {
		if _, ok := ja[k]; !ok {
			t.Errorf("key %q exists in en but not in ja", k)
		}
	}
}

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
	// 共有の ./logs ログインベースラインに書き込まない（テスト順序依存の防止）。
	cfg.SSHLoginBaselinePath = ""
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

// ---- SUID fast (high-risk directories) scan ----

// TestDefaultConfigCoversHighRiskSUIDPaths is the regression test for the
// detection gap found by attack 3 of the live test cycle: a SUID binary
// dropped in /tmp/kizuna-suid-test/ was never reported because the default
// SUIDPaths only contained /usr/* and the scan was gated to a single hour.
func TestDefaultConfigCoversHighRiskSUIDPaths(t *testing.T) {
	c := DefaultConfig()
	if !c.SUIDFastCheck {
		t.Fatal("suid_fast_check は既定で有効であるべき")
	}
	want := map[string]bool{"/tmp": true, "/var/tmp": true, "/dev/shm": true}
	for _, p := range c.SUIDFastPaths {
		delete(want, p)
	}
	if len(want) != 0 {
		t.Fatalf("高リスクパスが既定に含まれていません: 欠落=%v 実際=%v", want, c.SUIDFastPaths)
	}
	if c.SUIDFastScanInterval != 10 {
		t.Fatalf("suid_fast_interval_sec の既定 = %d, want 10", c.SUIDFastScanInterval)
	}
	if c.SUIDFastBaselinePath == "" || c.SUIDFastBaselinePath == c.SUIDBaselinePath {
		t.Fatalf("高速走査は専用のベースラインを使う必要があります: %q", c.SUIDFastBaselinePath)
	}
	if len(c.SUIDPaths) == 0 {
		t.Fatal("通常の SUIDPaths も維持されるべき")
	}
}

// TestParseConfigSUIDFast verifies the new keys, including the clamping of a
// too-small interval (a 1-second full walk would be a self-inflicted DoS).
func TestParseConfigSUIDFast(t *testing.T) {
	c, err := ParseConfig(map[string]interface{}{
		"suid_fast_check":         false,
		"suid_fast_paths":         "/tmp , /home\n/opt",
		"suid_fast_interval_sec":  float64(5),
		"suid_fast_baseline_path": "./logs/suid-fast.json",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	// Intervals are clamped in Validate (called from SecurityPlugin.Configure),
	// not in ParseConfig.
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.SUIDFastCheck {
		t.Error("suid_fast_check=false が反映されていません")
	}
	if strings.Join(c.SUIDFastPaths, ",") != "/tmp,/home,/opt" {
		t.Errorf("suid_fast_paths = %v", c.SUIDFastPaths)
	}
	if c.SUIDFastScanInterval != 5 {
		t.Errorf("5秒は下限なのでそのまま残るべき: %d", c.SUIDFastScanInterval)
	}
	if c.SUIDFastBaselinePath != "./logs/suid-fast.json" {
		t.Errorf("suid_fast_baseline_path = %q", c.SUIDFastBaselinePath)
	}

	// A sane value is kept as-is.
	c2, err := ParseConfig(map[string]interface{}{"suid_fast_interval_sec": float64(45)})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if err := c2.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c2.SUIDFastScanInterval != 45 {
		t.Errorf("suid_fast_interval_sec = %d, want 45", c2.SUIDFastScanInterval)
	}

	// A list is accepted too (the dashboard sends arrays).
	c3, err := ParseConfig(map[string]interface{}{"suid_fast_paths": []interface{}{"/tmp", "/srv"}})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if err := c3.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if strings.Join(c3.SUIDFastPaths, ",") != "/tmp,/srv" {
		t.Errorf("suid_fast_paths(list) = %v", c3.SUIDFastPaths)
	}

	// 下限未満は既定 10 秒へ戻す（1 秒ごとの全走査は自己 DoS になる）。
	c4, err := ParseConfig(map[string]interface{}{"suid_fast_interval_sec": float64(3)})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if err := c4.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c4.SUIDFastScanInterval != 10 {
		t.Errorf("3秒は既定の10秒へ丸めるべき: %d", c4.SUIDFastScanInterval)
	}

	// inotify 監視は既定で有効、対象は動きの速い置き場。
	if !c4.SUIDWatch {
		t.Error("suid_watch は既定で有効であるべき")
	}
	if strings.Join(c4.SUIDWatchPaths, ",") != "/tmp,/var/tmp,/dev/shm,/run" {
		t.Errorf("suid_watch_paths の既定 = %v", c4.SUIDWatchPaths)
	}

	// 明示指定は反映され、空指定は既定へ戻る（誤って即時監視を失わない）。
	c5, err := ParseConfig(map[string]interface{}{
		"suid_watch":       false,
		"suid_watch_paths": "/tmp\n/var/tmp",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if err := c5.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c5.SUIDWatch {
		t.Error("suid_watch=false が反映されていません")
	}
	if strings.Join(c5.SUIDWatchPaths, ",") != "/tmp,/var/tmp" {
		t.Errorf("suid_watch_paths = %v", c5.SUIDWatchPaths)
	}
	c6, err := ParseConfig(map[string]interface{}{"suid_watch_paths": ""})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if err := c6.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(c6.SUIDWatchPaths) != 4 {
		t.Errorf("空の suid_watch_paths は既定へ戻すべき: %v", c6.SUIDWatchPaths)
	}
}

// TestSUIDFastMonitorDetectsWithinInterval reproduces attack 3 without waiting
// for the hourly, hour-gated scan: a freshly written SUID file in a high-risk
// directory is reported critical on the very next check.
func TestSUIDFastMonitorDetectsWithinInterval(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "suid-fast.json")
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }

	// scanHour = -1 (no hour gate) and the configured 30s interval.
	sm := NewSUIDMonitor([]string{dir}, baseline, 30*time.Second, -1, "ja", nil, emit)
	sm.Check() // record the baseline (nothing there yet)
	if len(events) != 0 {
		t.Fatalf("baseline should be silent: %+v", events)
	}

	bin := filepath.Join(dir, "pwned")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}

	// The next poll happens less than 30s later, so emulate the elapsed window.
	sm.lastScan = time.Now().Add(-31 * time.Second)
	sm.Check()

	found := false
	for _, ev := range events {
		if ev.Category == "suid" && ev.Level == "critical" && ev.Source == bin {
			found = true
		}
	}
	if !found {
		t.Fatalf("高リスクディレクトリの新規 SUID ファイルを検知できませんでした: %+v", events)
	}
}

// TestSUIDCheckForceIgnoresInterval covers the event-driven entry point: an
// inotify event proves the tree changed, so the scan must not wait for the
// interval that throttles the periodic polling.
func TestSUIDCheckForceIgnoresInterval(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "suid-force.json")
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }

	sm := NewSUIDMonitor([]string{dir}, baseline, time.Hour, -1, "ja", nil, emit)
	sm.Check() // ベースライン記録（空）

	bin := filepath.Join(dir, "pwned")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}

	// 1時間の間隔が設定されているので、通常の Check はまだ走査しない。
	sm.Check()
	if len(events) != 0 {
		t.Fatalf("間隔内の Check は走査してはいけません: %+v", events)
	}

	// イベント駆動の CheckForce は間隔を無視して即座に検知する。
	sm.CheckForce()
	found := false
	for _, ev := range events {
		if ev.Category == "suid" && ev.Level == "critical" && ev.Source == bin {
			found = true
		}
	}
	if !found {
		t.Fatalf("CheckForce が新規 SUID を検知できませんでした: %+v", events)
	}
}

// TestSUIDWatcherTriggersImmediateScan is the regression test for the remaining
// attack 3 gap: a SUID file that is created and deleted between two scans is
// invisible to polling (measured: 2 misses in 7 with a 15s poll and a 10s
// window). The inotify watch must turn the creation into a scan right away.
func TestSUIDWatcherTriggersImmediateScan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("inotify は linux のみ")
	}
	dir := t.TempDir()
	baseline := filepath.Join(dir, "suid-watch.json")

	var mu sync.Mutex
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	sm := NewSUIDMonitor([]string{dir}, baseline, time.Hour, -1, "ja", nil, emit)
	sm.Check() // ベースライン記録（空）

	w := newSUIDWatcher([]string{dir}, nil, sm.CheckForce)
	if err := w.start(); err != nil {
		t.Skipf("inotify を開始できません（この環境では定期走査のみ）: %v", err)
	}
	defer w.stop()
	if !w.Running() {
		t.Fatal("inotify 監視が動作していません")
	}

	// 攻撃 3 と同じ手順: ファイルを作って chmod u+s。
	bin := filepath.Join(dir, "pwned")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}

	// 1時間間隔の走査設定でも、イベント駆動なら数秒以内に出る。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := false
		for _, ev := range events {
			if ev.Category == "suid" && ev.Level == "critical" && ev.Source == bin {
				found = true
			}
		}
		mu.Unlock()
		if found {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("inotify 経由で即時検知できませんでした: %+v", events)
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
