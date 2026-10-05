package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// 攻撃 A-5 の回帰テスト（2026-10-04）: /tmp/kizuna-fim-test/watched.txt の
// 作成→変更→削除が、integrity_files（事前に列挙した絶対パス）方式では
// 1 件も検知されなかった。FIMDirWatcher はディレクトリを単位に監視し、
// inotify が知らせたパスをその場でハッシュして差分を通知する。

// newFIMDirTestWatcher builds a watcher over dir and returns the collected
// events. The state file lives in a separate directory on purpose: it sits next
// to the watched tree in production (./logs) and would otherwise be reported as
// a change of the watched directory itself.
func newFIMDirTestWatcher(t *testing.T, dir string, ignore []string, maxFiles, maxSizeKB int) (*FIMDirWatcher, func() []module.SecurityEvent) {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "fim-dirs.json")
	var mu sync.Mutex
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}
	f := NewFIMDirWatcher([]string{dir}, ignore, statePath, 10, maxFiles, maxSizeKB, nil, emit, nil)
	take := func() []module.SecurityEvent {
		mu.Lock()
		defer mu.Unlock()
		out := events
		events = nil
		return out
	}
	return f, take
}

// fimDirEventCount counts the events of a level (empty = any) and source
// (empty = any).
func fimDirEventCount(events []module.SecurityEvent, level, source string) int {
	n := 0
	for _, ev := range events {
		if level != "" && ev.Level != level {
			continue
		}
		if source != "" && ev.Source != source {
			continue
		}
		n++
	}
	return n
}

// waitForFIMDirEvent drains the collected events until one matches.
func waitForFIMDirEvent(t *testing.T, take func() []module.SecurityEvent, match func(module.SecurityEvent) bool, timeout time.Duration) module.SecurityEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, ev := range take() {
			if match(ev) {
				return ev
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("期待したイベントが %s 以内に通知されませんでした", timeout)
	return module.SecurityEvent{}
}

// TestFIMDirWatcherBaselinesWithoutEvents: 最初の走査は静かにベースラインを
// 記録する（既存ファイルを「作成」として通知しない）。
func TestFIMDirWatcherBaselinesWithoutEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing.conf"), []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 0)

	f.Check()
	if got := take(); len(got) != 0 {
		t.Fatalf("初回走査は通知を出してはいけません: %+v", got)
	}

	// 変化が無ければ 2 回目以降も通知しない。
	f.Check()
	if got := take(); len(got) != 0 {
		t.Fatalf("変化の無い走査は通知を出してはいけません: %+v", got)
	}
}

// TestFIMDirWatcherDetectsCreateChangeDelete: 作成は critical、変更は
// critical、削除は warning（FIM 本体と同じ扱い）。
func TestFIMDirWatcherDetectsCreateChangeDelete(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 0)
	f.Check() // 空のベースライン

	target := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(target, []byte("baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.Check()
	events := take()
	if n := fimDirEventCount(events, "critical", target); n != 1 {
		t.Fatalf("作成は critical 1 件であるべきです: %+v", events)
	}
	if events[0].Category != "integrity" {
		t.Fatalf("category は integrity であるべきです: %+v", events[0])
	}

	if err := os.WriteFile(target, []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.Check()
	events = take()
	if n := fimDirEventCount(events, "critical", target); n != 1 {
		t.Fatalf("変更は critical 1 件であるべきです: %+v", events)
	}

	// 変更を検知したあと同じ内容なら再通知しない。
	f.Check()
	if got := take(); len(got) != 0 {
		t.Fatalf("同一内容での再通知は不要です: %+v", got)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	f.Check()
	events = take()
	if n := fimDirEventCount(events, "warning", target); n != 1 {
		t.Fatalf("削除は warning 1 件であるべきです: %+v", events)
	}
}

// TestFIMDirWatcherHandlePathsDetectsEventDrivenChanges: inotify から通知された
// パスだけを走査する経路（全体走査をしない）でも作成・変更・削除を検知する。
func TestFIMDirWatcherHandlePathsDetectsEventDrivenChanges(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 0)
	f.Check()

	target := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(target, []byte("baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandlePaths([]string{target})
	if n := fimDirEventCount(take(), "critical", target); n != 1 {
		t.Fatal("イベント経由の作成を検知できませんでした")
	}

	if err := os.WriteFile(target, []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandlePaths([]string{target})
	if n := fimDirEventCount(take(), "critical", target); n != 1 {
		t.Fatal("イベント経由の変更を検知できませんでした")
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	f.HandlePaths([]string{target})
	if n := fimDirEventCount(take(), "warning", target); n != 1 {
		t.Fatal("イベント経由の削除を検知できませんでした")
	}
}

// TestFIMDirWatcherDetectsShortLivedFile: 作成〜削除が走査の合間に完結した
// 場合でも、inotify が知らせたパスは残っているので「短命なファイル」として
// 通知する（内容のハッシュは取得できないため warning）。
func TestFIMDirWatcherDetectsShortLivedFile(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 0)
	f.Check()

	target := filepath.Join(dir, "flash.txt")
	if err := os.WriteFile(target, []byte("evil"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	f.HandlePaths([]string{target})

	events := take()
	if n := fimDirEventCount(events, "warning", target); n != 1 {
		t.Fatalf("短命なファイルは warning で通知されるべきです: %+v", events)
	}
}

// TestFIMDirWatcherSurvivesRestartWithoutFalseCreate: ベースラインは署名付きで
// 永続化されるため、再起動（新しいインスタンス）で「全ファイルが新規」とは
// ならない。
func TestFIMDirWatcherSurvivesRestartWithoutFalseCreate(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "fim-dirs.json")
	target := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	first := NewFIMDirWatcher([]string{dir}, nil, statePath, 10, 0, 0, nil, emit, nil)
	first.Check()
	if len(events) != 0 {
		t.Fatalf("初回走査は通知を出してはいけません: %+v", events)
	}

	mu.Lock()
	events = nil
	mu.Unlock()
	second := NewFIMDirWatcher([]string{dir}, nil, statePath, 10, 0, 0, nil, emit, nil)
	second.Check()
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Fatalf("再起動後に既存ファイルを再通知してはいけません: %+v", events)
	}
	if len(second.baseline) != 1 {
		t.Fatalf("ベースラインが復元されていません: %+v", second.baseline)
	}
}

// TestFIMDirWatcherIgnoresPatterns: fim_watch_ignore の glob はファイル名と
// パスの両方に照合する。
func TestFIMDirWatcherIgnoresPatterns(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, []string{"*.swp"}, 0, 0)
	f.Check()

	ignored := filepath.Join(dir, ".notes.swp")
	if err := os.WriteFile(ignored, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(kept, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f.Check()

	events := take()
	// 除外パターン一致は critical/warning を出さない（タスク3で info の
	// 可視化記録のみ追加）。除外されないファイルは critical で通知される。
	if n := fimDirEventCount(events, "critical", ignored); n != 0 {
		t.Fatalf("除外パターンに一致するファイルを重大通知してはいけません: %+v", events)
	}
	if n := fimDirEventCount(events, "critical", kept); n != 1 {
		t.Fatalf("除外されないファイルは通知されるべきです: %+v", events)
	}
}

// TestFIMDirWatcherCapsScanWithoutFalseDeletes: 上限に達した走査では、見て
// いないファイルを「削除」と誤判定せず、走査できていないことを 1 回だけ
// 通知する。
func TestFIMDirWatcherCapsScanWithoutFalseDeletes(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		name := filepath.Join(dir, "f"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// maxFiles は 16 が下限なので、20 ファイルでは上限に達する。
	f, take := newFIMDirTestWatcher(t, dir, nil, 16, 0)
	f.Check() // ベースライン（先頭 16 件だけ記録される）
	take()

	f.Check()
	events := take()
	if len(events) != 1 {
		t.Fatalf("走査不能の警告 1 件だけを期待しました: %+v", events)
	}
	if events[0].Level != "warning" {
		t.Fatalf("走査不能は warning であるべきです: %+v", events[0])
	}

	// 状態が変わらなければ繰り返し通知しない。
	f.Check()
	if got := take(); len(got) != 0 {
		t.Fatalf("同じ走査不能状態を繰り返し通知してはいけません: %+v", got)
	}
}

// TestFIMDirWatcherRejectsTamperedBaseline: 署名の合わないベースラインは
// critical で通知したうえで破棄し、取り直す（黙って受け入れて「改ざん無し」の
// 初期状態に戻せてしまうと監視の意味が無い）。
func TestFIMDirWatcherRejectsTamperedBaseline(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "fim-dirs.json")
	key := []byte("test-chain-key")
	if err := os.WriteFile(statePath, []byte(`{"baseline":{"/etc/passwd":{"hash":"deadbeef"}},"sig":"00"}`), 0600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	f := NewFIMDirWatcher([]string{dir}, nil, statePath, 10, 0, 0, nil, emit, key)
	mu.Lock()
	got := append([]module.SecurityEvent{}, events...)
	events = nil
	mu.Unlock()
	if n := fimDirEventCount(got, "critical", statePath); n != 1 {
		t.Fatalf("改ざんされたベースラインは critical 1 件で通知されるべきです: %+v", got)
	}

	// 取り直したベースラインは同じ鍵で署名され、次の起動では通知されない。
	f.Check()
	mu.Lock()
	events = nil
	mu.Unlock()
	f2 := NewFIMDirWatcher([]string{dir}, nil, statePath, 10, 0, 0, nil, emit, key)
	f2.Check()
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Fatalf("再取得したベースラインを改ざん扱いしてはいけません: %+v", events)
	}
}

// TestInotifyWatcherReportsChangedPaths: inotify 層は「何か変わった」だけでなく
// 「どのパスが変わったか」を通知する（FIM ディレクトリ監視がそのパスだけを
// ハッシュするために必要）。
func TestInotifyWatcherReportsChangedPaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("inotify は linux のみ")
	}
	dir := t.TempDir()
	got := make(chan []string, 8)
	w := newInotifyWatcher([]string{dir}, "FIM", nil, func(files, dirs []string) {
		select {
		case got <- files:
		default:
		}
	})
	if err := w.start(); err != nil {
		t.Skipf("inotify を開始できません（この環境では定期走査のみ）: %v", err)
	}
	defer w.stop()

	target := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case paths := <-got:
			for _, p := range paths {
				if p == target {
					return
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("inotify が変更されたパスを通知しませんでした")
}

// TestInotifyWatcherWatchesAncestorOfMissingRoot: 設定された監視ルートがまだ
// 存在しない場合（攻撃者が最初にディレクトリを作る形）でも、最も近い既存の親を
// 監視して作成を捉える。配備時に「0 個のディレクトリを監視」となり、最初の
// 攻撃（ディレクトリ作成から始まる）を取りこぼした実例の回帰テスト。
func TestInotifyWatcherWatchesAncestorOfMissingRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("inotify は linux のみ")
	}
	parent := t.TempDir()
	missing := filepath.Join(parent, "incoming")
	got := make(chan []string, 8)
	w := newInotifyWatcher([]string{missing}, "FIM", nil, func(files, dirs []string) {
		select {
		case got <- dirs:
		default:
		}
	})
	if err := w.start(); err != nil {
		t.Skipf("inotify を開始できません（この環境では定期走査のみ）: %v", err)
	}
	defer w.stop()

	if err := os.Mkdir(missing, 0700); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case dirs := <-got:
			for _, p := range dirs {
				if p == missing {
					return
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("存在しない監視ルートの作成を inotify が通知しませんでした")
}

// TestFIMDirWatcherHandleChangesScansNewDirectory: 新しく作られたディレクトリが
// 通知されたら、その中を走査する。inotify の監視を張る前にファイルが作られた
// 場合（mkdir 直後のリダイレクト）でも取りこぼさない。
func TestFIMDirWatcherHandleChangesScansNewDirectory(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 0)
	f.Check()

	sub := filepath.Join(dir, "fresh")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "watched.txt")
	if err := os.WriteFile(target, []byte("evil"), 0600); err != nil {
		t.Fatal(err)
	}

	// ファイルの作成イベントは取りこぼした（監視設置前だった）状況を再現する。
	f.HandleChanges([]string{}, []string{sub})

	events := take()
	if n := fimDirEventCount(events, "critical", target); n != 1 {
		t.Fatalf("新規ディレクトリ内のファイル作成を critical で検知するべきです: %+v", events)
	}
}

// TestFIMDirWatcherIgnoresDirectoryEvents: ディレクトリの作成・削除を
// 「短命なファイル」として誤報しない（実機で出た誤報の回帰）。
func TestFIMDirWatcherIgnoresDirectoryEvents(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 0)
	f.Check()

	// 空のディレクトリを作って削除する。
	sub := filepath.Join(dir, "empty")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{}, []string{sub})
	if got := take(); len(got) != 0 {
		t.Fatalf("空ディレクトリの作成を通知してはいけません: %+v", got)
	}

	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{}, []string{sub})
	if got := take(); len(got) != 0 {
		t.Fatalf("ディレクトリの削除を「短命なファイル」として通知してはいけません: %+v", got)
	}
}

// TestFIMDirWatcherDetectsAttackA5ViaInotify is the end-to-end regression test
// for the reported gap: 実機の攻撃 A-5 と同じ手順（作成→変更→削除）を、
// 周期走査に依存せず inotify 経由で検知できることを固定する。
func TestFIMDirWatcherDetectsAttackA5ViaInotify(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("inotify は linux のみ")
	}
	dir := t.TempDir()
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 0)
	f.Check() // ベースライン（空）

	w := newInotifyWatcher([]string{dir}, "FIM", nil, f.HandleChanges)
	if err := w.start(); err != nil {
		t.Skipf("inotify を開始できません（この環境では定期走査のみ）: %v", err)
	}
	defer w.stop()

	target := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForFIMDirEvent(t, take, func(ev module.SecurityEvent) bool {
		return ev.Source == target && ev.Level == "critical" &&
			ev.Title == msg("ja", "fimwatch.create.title")
	}, 5*time.Second)

	// 改ざん（内容の変更）はファイル本体の FIM と同じく critical。
	if err := os.WriteFile(target, []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForFIMDirEvent(t, take, func(ev module.SecurityEvent) bool {
		return ev.Source == target && ev.Level == "critical" &&
			ev.Title == msg("ja", "fimwatch.change.title")
	}, 5*time.Second)

	// 削除は warning。
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	waitForFIMDirEvent(t, take, func(ev module.SecurityEvent) bool {
		return ev.Source == target && ev.Level == "warning" &&
			ev.Title == msg("ja", "fimwatch.delete.title")
	}, 5*time.Second)
}
