package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// --- F-3: ss の絶対パス解決 ---

// 候補は絶対パスのみであること（PATH 依存を残さない）。
func TestSSCandidatesAreAbsolute(t *testing.T) {
	for _, c := range ssCandidates {
		if !filepath.IsAbs(c) {
			t.Fatalf("ss candidate %q must be absolute", c)
		}
	}
	if p := resolveSSPath(); p != "" && !filepath.IsAbs(p) {
		t.Fatalf("resolveSSPath() = %q, want an absolute path or empty", p)
	}
}

// F-3: ss を実行できない状態が続いても、通知は1回だけ（毎回の警報を防ぐ）。
func TestPortMonitorNotifiesOnceWhenSSFails(t *testing.T) {
	orig := listPortsFn
	defer func() { listPortsFn = orig }()
	listPortsFn = func() ([]portInfo, error) { return nil, os.ErrNotExist }

	var events []module.SecurityEvent
	pm := NewPortMonitor(filepath.Join(t.TempDir(), "ports.json"), nil, func(ev module.SecurityEvent) {
		events = append(events, ev)
	})

	pm.Check()
	pm.Check()

	if len(events) != 1 {
		t.Fatalf("want exactly one alert while ss keeps failing, got %d (%+v)", len(events), events)
	}
	if events[0].Level != "warning" || events[0].Source != "ss" {
		t.Fatalf("unexpected alert: %+v", events[0])
	}
}

// --- F-4: FIM ベースライン保護 ---

// 監視対象が読めなくなったら警告し、ベースラインから消さないこと。
func TestFIMWarnsWhenWatchedFileBecomesUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 0000 files, so the unreadable case cannot be reproduced")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := os.WriteFile(target, []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "fim.json")

	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }
	fim := NewFIM([]string{target}, baseline, nil, emit)

	fim.Check() // 初回はベースライン記録のみ
	events = nil

	if err := os.Chmod(target, 0); err != nil {
		t.Fatal(err)
	}
	fim.Check()
	if len(events) != 1 {
		t.Fatalf("unreadable watched file must raise exactly one alert, got %d (%+v)", len(events), events)
	}
	// 権限不足（chmod 000）は非 root 運用で常時発生するため info へ降格した。
	// 監視不能を知らせること自体は維持する（黙って落とさない）。
	if events[0].Level != "info" || events[0].Source != target {
		t.Fatalf("unexpected alert: %+v", events[0])
	}

	fim.mu.Lock()
	_, kept := fim.baseline[target]
	fim.mu.Unlock()
	if !kept {
		t.Fatal("an unreadable file must stay in the baseline")
	}

	events = nil
	fim.Check()
	if len(events) != 0 {
		t.Fatalf("repeat alerts must be suppressed, got %+v", events)
	}
	_ = os.Chmod(target, 0600)
}

// ベースラインを書き換えても署名で検知できること。
func TestFIMDetectsTamperedBaseline(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := os.WriteFile(target, []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "fim.json")
	key := []byte("test-chain-key-0123456789abcdef")

	fim := NewFIM([]string{target}, baseline, nil, func(module.SecurityEvent) {})
	fim.SetChainKey(key)
	fim.Check()

	data, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatal(err)
	}
	var st fimState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st.Sig == "" {
		t.Fatal("baseline must be signed")
	}
	if want := signFimState(key, st); want != st.Sig {
		t.Fatalf("signature mismatch: got %q want %q", st.Sig, want)
	}

	// 攻撃者がベースラインを書き換えて「改ざん無し」に見せかける。
	st.Baseline[target] = strings.Repeat("0", 64)
	tampered, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseline, tampered, 0600); err != nil {
		t.Fatal(err)
	}

	var second []module.SecurityEvent
	fim2 := NewFIM([]string{target}, baseline, nil, func(ev module.SecurityEvent) {
		second = append(second, ev)
	})
	fim2.SetChainKey(key)
	if fim2.initialized {
		t.Fatal("a tampered baseline must not be trusted")
	}
	if len(second) != 1 || second[0].Level != "critical" {
		t.Fatalf("tampered baseline must raise a critical alert, got %+v", second)
	}

	// 再取得後は正しい署名付きベースラインとして受け入れられること。
	fim2.Check()
	fim3 := NewFIM([]string{target}, baseline, nil, nil)
	fim3.SetChainKey(key)
	if !fim3.initialized {
		t.Fatal("a correctly signed baseline must be trusted")
	}

	// 鍵を設定している運用では、署名の無い旧形式は受け入れないこと。
	flat, err := json.Marshal(map[string]string{target: strings.Repeat("1", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseline, flat, 0600); err != nil {
		t.Fatal(err)
	}
	fim4 := NewFIM([]string{target}, baseline, nil, nil)
	fim4.SetChainKey(key)
	if fim4.initialized {
		t.Fatal("an unsigned (legacy) baseline must be rejected when a key is configured")
	}
}

// F-4: 鍵を先に渡して読み込めば、正しく署名されたベースラインを再起動時に
// 誤って「改ざん」と通知しないこと（鍵付き運用での再起動ごとの誤報を防ぐ）。
func TestNewFIMKeyedTrustsSignedBaselineWithoutAlert(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := os.WriteFile(target, []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "fim.json")
	key := []byte("test-chain-key-0123456789abcdef")

	// 鍵付きでベースラインを記録する。
	first := NewFIMKeyed([]string{target}, baseline, nil, func(module.SecurityEvent) {}, key)
	first.Check()
	if !first.initialized {
		t.Fatal("first run must record a baseline")
	}

	// 「再起動」= 同じ鍵で読み直す。誤報が出ないこと。
	var events []module.SecurityEvent
	second := NewFIMKeyed([]string{target}, baseline, nil, func(ev module.SecurityEvent) {
		events = append(events, ev)
	}, key)
	if !second.initialized {
		t.Fatal("a correctly signed baseline must be trusted on reload")
	}
	if len(events) != 0 {
		t.Fatalf("reload with the correct key must not alert, got %+v", events)
	}
}

// --- F-5: ログローテーション（inode 差し替え）の取りこぼし防止 ---

func TestMonitorRereadsRotatedLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dpkg.log")
	if err := os.WriteFile(path, []byte("2026-10-04 12:00:00 install basepkg\n"), 0600); err != nil {
		t.Fatal(err)
	}

	m := NewMonitor(&SecurityConfig{
		Language:      "ja",
		WatchFiles:    []string{path},
		NotifyMinimal: "info",
	}, nil, nil)
	defer m.Stop()

	m.Scan() // 初回は現在位置から（既存行は読まない）

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("2026-10-04 12:00:01 install appended\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m.Scan()
	if got := countInstallEvents(m.Drain()); got != 1 {
		t.Fatalf("append should yield 1 install event, got %d", got)
	}

	// ローテーション: 別 inode の新しいファイルに置き換える。新しいファイルの
	// 方が大きいので、offset の比較だけでは検出できない。
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&sb, "2026-10-04 12:00:0%d install rotated%d\n", i+2, i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0600); err != nil {
		t.Fatal(err)
	}

	m.Scan()
	if got := countInstallEvents(m.Drain()); got != 3 {
		t.Fatalf("rotated log must be read from the start: want 3 install events, got %d", got)
	}
}

func countInstallEvents(events []module.SecurityEvent) int {
	n := 0
	for _, ev := range events {
		if ev.Category == "install" {
			n++
		}
	}
	return n
}

// --- F-2: セキュリティログの末尾削除（巻き戻し）検知 ---

func TestCheckLogMonotonicDetectsTruncation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kizuna-security.log")
	statePath := logStatePathFor(logPath)

	if err := os.WriteFile(logPath, []byte("{}\n{}\n{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if reason, _ := checkLogMonotonic(logPath, statePath); reason != "" {
		t.Fatalf("first run must be clean, got %q", reason)
	}
	// 追記は問題なし。
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{}\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if reason, _ := checkLogMonotonic(logPath, statePath); reason != "" {
		t.Fatalf("append must be clean, got %q", reason)
	}

	// 末尾を削除 → critical
	if err := os.WriteFile(logPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reason, level := checkLogMonotonic(logPath, statePath)
	if reason == "" || level != "critical" {
		t.Fatalf("truncation must be critical, got (%q,%q)", reason, level)
	}
}

// ログ自体が消えた場合も critical とすること。
func TestCheckLogMonotonicDetectsRemoval(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kizuna-security.log")
	statePath := logStatePathFor(logPath)

	if err := os.WriteFile(logPath, []byte("{}\n{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if reason, _ := checkLogMonotonic(logPath, statePath); reason != "" {
		t.Fatalf("first run must be clean, got %q", reason)
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	reason, level := checkLogMonotonic(logPath, statePath)
	if reason == "" || level != "critical" {
		t.Fatalf("removal must be critical, got (%q,%q)", reason, level)
	}
}

// 読取不能の通知は再起動で繰り返さず、既定24時間の間隔でのみ再通知すること。
// 非 root 運用では /etc/shadow 等が常に読めず、再起動のたびに警告を出すと
// ログと Discord が埋まり本物の異常が見えなくなる（ノイズ削減）。
func TestFIMUnreadableAlertIsRateLimitedAcrossRestarts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 0000 files, so the unreadable case cannot be reproduced")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := os.WriteFile(target, []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(dir, "fim.json")

	old := fimUnreadableWarnInterval
	fimUnreadableWarnInterval = 24 * time.Hour
	defer func() { fimUnreadableWarnInterval = old }()

	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }

	fim := NewFIM([]string{target}, baseline, nil, emit)
	fim.Check()
	events = nil
	if err := os.Chmod(target, 0); err != nil {
		t.Fatal(err)
	}
	fim.Check()
	if len(events) != 1 || events[0].Level != "info" {
		t.Fatalf("the first sighting must emit exactly one info alert, got %+v", events)
	}

	// 再起動（同じベースラインを読み直す）では繰り返さない。
	events = nil
	restarted := NewFIM([]string{target}, baseline, nil, emit)
	restarted.Check()
	if len(events) != 0 {
		t.Fatalf("a restart must not repeat the unreadable alert, got %+v", events)
	}

	// 間隔が過ぎたら再通知する（監視不能を放置しない）。
	fimUnreadableWarnInterval = 0
	events = nil
	later := NewFIM([]string{target}, baseline, nil, emit)
	later.Check()
	if len(events) != 1 {
		t.Fatalf("after the interval the alert must reappear, got %+v", events)
	}

	// 読めるように戻ったら履歴を消し、次に読めなくなったら即座に通知する。
	if err := os.Chmod(target, 0600); err != nil {
		t.Fatal(err)
	}
	later.Check()
	events = nil
	if err := os.Chmod(target, 0); err != nil {
		t.Fatal(err)
	}
	later.Check()
	if len(events) != 1 {
		t.Fatalf("becoming unreadable again must alert immediately, got %+v", events)
	}
	_ = os.Chmod(target, 0600)
}

// 自作の読み取り専用ヘルパーの sudo 実行は、設定に関わらず通知対象外にすること。
// これを通知するとポーリングごとに WARNING が出て、本物の sudo が見えなくなる。
func TestSudoHelperIsAlwaysIgnored(t *testing.T) {
	m := &Monitor{cfg: DefaultConfig()}
	if !m.isIgnoredSudo(cronSudoHelper) {
		t.Fatal("the plugin's own read-only helper must be ignored")
	}
	if !m.isIgnoredSudo(cronSudoHelper + " --json") {
		t.Fatal("the helper must be ignored when it is run with arguments")
	}
	if !m.isIgnoredSudo(" " + cronSudoHelper) {
		t.Fatal("surrounding whitespace must not defeat the ignore rule")
	}

	// 設定で除外リストを空にしても、自作ヘルパーだけは無視し続ける。
	empty := &Monitor{cfg: &SecurityConfig{}}
	if !empty.isIgnoredSudo(cronSudoHelper) {
		t.Fatal("the helper must be ignored even when sudo_ignore_commands is empty")
	}
	// 除外しすぎないこと（無関係なコマンドと紛らわしいパスは通知する）。
	if empty.isIgnoredSudo("/usr/bin/id") {
		t.Fatal("unrelated sudo commands must still be alerted")
	}
	if empty.isIgnoredSudo(cronSudoHelper + "-lookalike.sh") {
		t.Fatal("a look-alike path must not be ignored")
	}
}
