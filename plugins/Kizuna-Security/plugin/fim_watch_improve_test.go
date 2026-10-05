package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// 攻撃テスト（2026-10-04）の回帰テスト。変種1〜4 と実装改善1〜4 を対象にする。
//   変種1 / 改善1: 深い階層（inotify 経路が「ファイル自身の深さ」で落としていた）
//   変種2 / 改善2: 大容量ファイル（存在すら通知されず、逆に「消えた」と誤報）
//   変種3 / 改善3: symlink 差し替え（リンク先を記録していなかった）
//   改善4:        監視ディレクトリ数の上限が黙って効いていた

// newFIMDirTestWatcherOpts is newFIMDirTestWatcher with the new options.
func newFIMDirTestWatcherOpts(t *testing.T, dir string, ignore []string, maxFiles, maxSizeKB int, opts ...fimDirOption) (*FIMDirWatcher, func() []module.SecurityEvent) {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "fim-dirs.json")
	var mu sync.Mutex
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}
	f := NewFIMDirWatcher([]string{dir}, ignore, statePath, 10, maxFiles, maxSizeKB, nil, emit, nil, opts...)
	take := func() []module.SecurityEvent {
		mu.Lock()
		defer mu.Unlock()
		out := events
		events = nil
		return out
	}
	return f, take
}

// fimDirHasTitle reports whether events contain the message key (ja) for source.
func fimDirHasTitle(events []module.SecurityEvent, titleKey, source string) bool {
	want := msg("ja", titleKey)
	for _, ev := range events {
		if ev.Title != want {
			continue
		}
		if source == "" || ev.Source == source {
			return true
		}
	}
	return false
}

// 変種1 / 改善1: 深い階層のファイルを inotify 経路でも検知する。以前は
// 「ファイル自身の深さ (4) > fimDirMaxDepth (3)」で落としていたため、周期走査が
// 拾うまで（10 秒未満で消えると永久に）無言だった。
func TestFIMDirWatcherDetectsDeepFileOnInotifyPath(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "sub1", "sub2", "sub3")
	if err := os.MkdirAll(deep, 0700); err != nil {
		t.Fatal(err)
	}
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 0)
	f.Check() // ベースライン（空）
	take()

	target := filepath.Join(deep, "watched.txt")
	if err := os.WriteFile(target, []byte("evil"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{target}, nil)
	events := take()
	if n := fimDirEventCount(events, "critical", target); n != 1 {
		t.Fatalf("深い階層の作成は inotify 経路で critical 1 件であるべきです: %+v", events)
	}
}

// 改善1: 深さ上限を超えるパスは「無言で未検知」ではなく、1 パス 1 回 warning に
// する。深さ上限は設定で変えられる（深さ 0 = ルート直下のみ）。
func TestFIMDirWatcherDepthLimitWarnsOnce(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "deep")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	f, take := newFIMDirTestWatcherOpts(t, dir, nil, 0, 0, WithFIMDirMaxDepth(0))
	if f.maxDepth != 0 {
		t.Fatalf("深さ上限が設定されていません: %d", f.maxDepth)
	}
	f.Check()
	take()

	// ルート直下は従来どおり検知する。
	rootFile := filepath.Join(dir, "root.txt")
	if err := os.WriteFile(rootFile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{rootFile}, nil)
	if !fimDirHasTitle(take(), "fimwatch.create.title", rootFile) {
		t.Fatal("ルート直下のファイルは検知されるべきです")
	}

	// 範囲外のファイルは落とすだけでなく warning で知らせる。
	deepFile := filepath.Join(sub, "hidden.txt")
	if err := os.WriteFile(deepFile, []byte("evil"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{deepFile}, nil)
	events := take()
	if !fimDirHasTitle(events, "fimwatch.depth.title", deepFile) {
		t.Fatalf("深さ上限超過は warning で通知するべきです: %+v", events)
	}
	if fimDirHasTitle(events, "fimwatch.transient.title", deepFile) {
		t.Fatalf("深さ上限の超過を「短命なファイル」と誤報してはいけません: %+v", events)
	}

	// 同じパスを毎回通知しない（深いツリーで埋まらないように）。
	f.HandleChanges([]string{deepFile}, nil)
	if got := take(); len(got) != 0 {
		t.Fatalf("同じパスの深さ警告を繰り返してはいけません: %+v", got)
	}
}

// 変種2 / 改善2: サイズ上限を超えるファイルは、内容ハッシュの代わりに
// サイズ・mtime・inode を記録して、作成/変更/削除を検知する。以前は
// ベースラインに入れず「走査する前に削除されました」と誤報していた。
func TestFIMDirWatcherOversizedFileIsTrackedByMetadata(t *testing.T) {
	dir := t.TempDir()
	// maxSizeKB=1 → 1KB を超えるファイルはハッシュしない。
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 1)
	f.Check()
	take()

	target := filepath.Join(dir, "large.txt")
	payload := strings.Repeat("A", 4096)
	if err := os.WriteFile(target, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}

	f.HandleChanges([]string{target}, nil)
	events := take()
	if !fimDirHasTitle(events, "fimwatch.large.create.title", target) {
		t.Fatalf("大容量ファイルの作成は warning で通知するべきです: %+v", events)
	}
	if fimDirHasTitle(events, "fimwatch.transient.title", target) {
		t.Fatalf("存在するファイルを「消えた」と誤報してはいけません: %+v", events)
	}

	f.mu.Lock()
	entry := f.baseline[target]
	f.mu.Unlock()
	if entry.Kind != kindLarge || entry.Hash != "" || entry.Size != int64(len(payload)) || entry.MTime == 0 {
		t.Fatalf("大容量ファイルはメタ情報で記録するべきです: %+v", entry)
	}

	// mtime だけが変わった場合は warning（メタ情報の変化）。
	// head を変えないので critical にはならない。
	time.Sleep(10 * time.Millisecond)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(target, future, future); err != nil {
		t.Fatal(err)
	}
	f.Check()
	events = take()
	if !fimDirHasTitle(events, "fimwatch.large.change.title", target) {
		t.Fatalf("メタ情報のみの変化は warning で通知するべきです: %+v", events)
	}

	// 先頭（内容）を書き換えた場合は critical（head ハッシュの変化）。
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(target, []byte(strings.Repeat("B", 4096)), 0600); err != nil {
		t.Fatal(err)
	}
	f.Check()
	events = take()
	if !fimDirHasTitle(events, "fimwatch.large.head.title", target) {
		t.Fatalf("大容量ファイルの先頭変更は critical で通知するべきです: %+v", events)
	}

	// 削除も検知する（以前は「存在しない」扱いで 1 件も出なかった）。
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	f.Check()
	events = take()
	if !fimDirHasTitle(events, "fimwatch.delete.title", target) {
		t.Fatalf("大容量ファイルの削除は warning で通知するべきです: %+v", events)
	}
}

// 変種3 / 改善3: symlink はリンク先を記録する。通常ファイル → symlink の
// 差し替え（内容偽装）は critical、リンク先の差し替えも critical。
func TestFIMDirWatcherSymlinkSwapIsCritical(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, take := newFIMDirTestWatcher(t, dir, nil, 0, 0)
	f.Check() // 通常ファイルとしてベースライン
	take()

	// 通常ファイル → /etc/passwd への symlink。
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", target); err != nil {
		t.Skipf("symlink を作成できません: %v", err)
	}
	f.HandleChanges([]string{target}, nil)
	events := take()
	if !fimDirHasTitle(events, "fimwatch.symlink.swap.title", target) {
		t.Fatalf("通常ファイル→symlink の差し替えは critical であるべきです: %+v", events)
	}
	if n := fimDirEventCount(events, "critical", target); n != 1 {
		t.Fatalf("差し替えは critical 1 件であるべきです: %+v", events)
	}

	// リンク先の差し替え（/etc/shadow へ）。
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", target); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{target}, nil)
	events = take()
	if !fimDirHasTitle(events, "fimwatch.symlink.target.title", target) {
		t.Fatalf("リンク先の差し替えは critical であるべきです: %+v", events)
	}

	// 新規の symlink は warning（通常ファイルの作成ほど強い信号ではない）。
	link2 := filepath.Join(dir, "link2.txt")
	if err := os.Symlink("/etc/hosts", link2); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{link2}, nil)
	events = take()
	if !fimDirHasTitle(events, "fimwatch.symlink.new.title", link2) {
		t.Fatalf("symlink の新規作成は warning で通知するべきです: %+v", events)
	}
}

// 改善（応用）: 除外ディレクトリ（systemd-private-* など）の中のファイルは、
// inotify 経路でも除外する。ファイル名だけを照合すると /tmp のノイズで埋まる。
func TestFIMDirWatcherIgnoresFilesInsideIgnoredDirectory(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "systemd-private-abc")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	f, take := newFIMDirTestWatcher(t, dir, []string{"systemd-private-*"}, 0, 0)
	f.Check()
	take()

	inside := filepath.Join(spool, "tmp.log")
	if err := os.WriteFile(inside, []byte("noise"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{inside}, nil)
	// 除外対象は critical/warning を出さない（タスク3で info の可視化
	// 記録だけを追加した。誤って改ざん扱いしないことを確認する）。
	events := take()
	for _, ev := range events {
		if ev.Level == "critical" || ev.Level == "warning" {
			t.Fatalf("除外ディレクトリ直下のファイルを重大通知してはいけません: %+v", ev)
		}
	}
}

// 改善4: ルート毎の監視ディレクトリ数上限に達したら、通知（warning）にする。
func TestFIMDirWatcherWarnWatchDirsEmitsWarning(t *testing.T) {
	dir := t.TempDir()
	f, take := newFIMDirTestWatcherOpts(t, dir, nil, 0, 0, WithFIMDirMaxDirs(4, nil))
	if f.maxDirs != 4 {
		t.Fatalf("監視ディレクトリ数の上限が設定されていません: %d", f.maxDirs)
	}
	f.WarnWatchDirs(dir, 4)
	events := take()
	if !fimDirHasTitle(events, "fimwatch.dircap.title", dir) {
		t.Fatalf("監視ディレクトリ数の上限到達は warning で通知するべきです: %+v", events)
	}
	if n := fimDirEventCount(events, "warning", dir); n != 1 {
		t.Fatalf("warning 1 件であるべきです: %+v", events)
	}
}

// 改善4: inotify 層のルート毎上限。上限に達したルートは 1 回だけコールバックし、
// それ以上 watch を張らない（黙って即時検知が止まるのを防ぐ）。
func TestInotifyWatcherPerRootCapWarnsOnce(t *testing.T) {
	root := t.TempDir()
	var got []string
	w := newInotifyWatcher([]string{root}, "FIM", nil, nil,
		withMaxDepth(1),
		withMaxDirsPerRoot(1, func(r string, limit int) { got = append(got, r) }))
	if w.maxDepth != 1 || w.maxDirsPerRoot != 1 {
		t.Fatalf("オプションが反映されていません: depth=%d dirs=%d", w.maxDepth, w.maxDirsPerRoot)
	}

	// inotify を開かずに「既に 1 本 watch がある」状態を作り、上限判定だけを見る。
	w.mu.Lock()
	w.wdToPath[1] = root
	w.mu.Unlock()

	w.addWatch(filepath.Join(root, "sub"))
	w.addWatch(filepath.Join(root, "sub2"))
	if len(got) != 1 || got[0] != root {
		t.Fatalf("ルート毎の上限通知は 1 回だけ出るべきです: %+v", got)
	}
}

// 改善1/改善4: fim_watch_max_depth / fim_watch_max_dirs の parse と安全弁。
func TestConfigFIMWatchDepthAndDirs(t *testing.T) {
	cfg, err := ParseConfig(map[string]interface{}{
		"fim_watch_max_depth": float64(5),
		"fim_watch_max_dirs":  float64(64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FIMWatchMaxDepth != 5 || cfg.FIMWatchMaxDirs != 64 {
		t.Fatalf("設定値が反映されていません: depth=%d dirs=%d", cfg.FIMWatchMaxDepth, cfg.FIMWatchMaxDirs)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.FIMWatchMaxDepth != 5 || cfg.FIMWatchMaxDirs != 64 {
		t.Fatalf("妥当な値は丸めてはいけません: depth=%d dirs=%d", cfg.FIMWatchMaxDepth, cfg.FIMWatchMaxDirs)
	}

	// 範囲外は既定値に戻す（inotify の watch 予算と深さの暴走を防ぐ安全弁）。
	bad, err := ParseConfig(map[string]interface{}{
		"fim_watch_max_depth": float64(99),
		"fim_watch_max_dirs":  float64(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Validate(); err != nil {
		t.Fatal(err)
	}
	if bad.FIMWatchMaxDepth != 3 || bad.FIMWatchMaxDirs != 1024 {
		t.Fatalf("範囲外の値は既定値へ戻すべきです: depth=%d dirs=%d", bad.FIMWatchMaxDepth, bad.FIMWatchMaxDirs)
	}

	def := DefaultConfig()
	if def.FIMWatchMaxDepth != 3 || def.FIMWatchMaxDirs != 1024 {
		t.Fatalf("既定値は 3 / 1024 であるべきです: depth=%d dirs=%d", def.FIMWatchMaxDepth, def.FIMWatchMaxDirs)
	}
}

// 改善2/警告の質: degraded（監視ディレクトリの一部を走査できません）の状態判定は
// 全体走査だけで行う。イベント走査は通知されたパスしか見ていないので、そこで
// 大きすぎ=0 でも状態を false に戻すと、周期走査のたびに 16 件 → 0 件 → 16 件 と
// 反転して警告が増える（実機 /tmp で 4 秒間隔の反転を観測）。
func TestFIMDirWatcherDegradedStateOnlyChangesOnFullScan(t *testing.T) {
	dir := t.TempDir()
	// maxSizeKB=1 → 1KB 超は「大きすぎて除外」扱いになる。
	f, take := newFIMDirTestWatcherOpts(t, dir, nil, 0, 1)
	f.Check() // ベースライン（空）
	take()

	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 4096), 0600); err != nil {
		t.Fatal(err)
	}
	f.Check() // 全体走査: 大きすぎるファイルが現れた
	if events := take(); !fimDirHasTitle(events, "fimwatch.degraded.title", "") {
		t.Fatalf("全体走査では「一部を走査できません」を通知するべきです: %+v", events)
	}

	// イベント走査（通知されたパスだけの走査）で状態を戻してはいけない。
	normal := filepath.Join(dir, "normal.txt")
	if err := os.WriteFile(normal, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	f.HandleChanges([]string{normal}, nil)
	if events := take(); fimDirHasTitle(events, "fimwatch.degraded.title", "") {
		t.Fatalf("イベント走査で degraded を通知してはいけません: %+v", events)
	}

	// 状態が変わらない全体走査でも再通知しない（毎周期の再通知を防ぐ）。
	f.Check()
	if events := take(); fimDirHasTitle(events, "fimwatch.degraded.title", "") {
		t.Fatalf("状態が同じなら全体走査でも再通知してはいけません: %+v", events)
	}

	// 原因が消えたら 1 回だけ戻りを通知する。
	if err := os.Remove(big); err != nil {
		t.Fatal(err)
	}
	f.Check()
	if events := take(); !fimDirHasTitle(events, "fimwatch.degraded.title", "") {
		t.Fatalf("解消時は 1 回通知するべきです: %+v", events)
	}
}
