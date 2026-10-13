package main

import (
	"crypto/subtle"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/module"
)

// 攻撃 A-5 の回帰（2026-10-04）: FIM は「絶対パスを列挙してハッシュ比較する」
// 方式なので、攻撃者が任意の名前でファイルを作る置き場（/tmp 等）は原理的に
// 監視できない。実機テストでは /tmp/kizuna-fim-test/watched.txt の
// 作成→変更→削除が、FIM の走査が 2 回入っていた（05:41:51 と 05:42:06）に
// もかかわらず 1 件も検知されなかった。監視対象に無いパスはハッシュされない
// ためで、検知漏れの原因は「走査タイミング」ではなく「監視の単位」にあった。
//
// FIMDirWatcher は監視の単位をディレクトリにする。inotify が通知したパスを
// その場でハッシュしてベースラインと比較するので、走査と走査の間で完結する
// 作成〜削除も拾える（イベントが来た時点のパスを覚えているため、走査の
// タイミングに依存しない）。周期走査は、inotify が使えない環境と、通知を
// 取りこぼした場合（キュー溢れ）の回収のためにフォールバックとして回す。
const (
	// fimDirMaxDepth is how deep below a watched root files are hashed.
	// 深いツリーを丸ごとハッシュすると 1 回の走査が重くなるため制限する。
	fimDirMaxDepth = 3
	// fimDirEventCap caps the per-file events one scan emits. 編集器や make が
	// 走るディレクトリでは 1 回の通知で数百件が変わるため、上限を超えた分は
	// 個別通知を省略して 1 件の要約にまとめる（本物の異常が埋もれるのを防ぐ）。
	fimDirEventCap = 20
)

// fimDirEntry is the recorded state of one file.
type fimDirEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// fimDirState is the persisted baseline of the directory watch. It is signed
// with the same key/rule as the file baseline (F-4): これを書き換えるだけで
// 「改ざん無し」の初期状態に戻せてしまうと、監視を無効化できてしまう。
type fimDirState struct {
	Baseline map[string]fimDirEntry `json:"baseline"`
	Roots    []string               `json:"roots,omitempty"`
	Sig      string                 `json:"sig,omitempty"`
}

// FIMDirWatcher detects creation, modification and deletion of files inside the
// configured directories. 通知は category "integrity" で出し、FIM 本体と
// 同じ通知経路（監視対象に後から追加したファイルは warning、変更は critical、
// 削除は warning）に合わせている。
type FIMDirWatcher struct {
	roots        []string
	ignore       []string
	baselinePath string
	interval     time.Duration
	maxFiles     int
	maxSize      int64
	logger       module.Logger
	emitFn       func(module.SecurityEvent)
	key          []byte

	mu          sync.Mutex
	baseline    map[string]fimDirEntry
	initialized bool
	// degraded records whether the last scan could not cover everything
	// (上限到達・大きすぎるファイル・読み取り不能）。状態が変わった時だけ
	// 通知する（毎回通知すると /tmp では履歴が埋まる）。
	degraded bool

	// langMu guards language only, for the same reason as FIM.langMu: scan()
	// holds mu while building messages.
	langMu   sync.RWMutex
	language string
}

// NewFIMDirWatcher builds the directory watch.
//
// maxFiles bounds how many files one scan hashes, maxSizeKB bounds the size of
// a hashed file (0 = no limit). ignore is a list of glob patterns matched
// against the base name and the full path (e.g. "*.swp").
func NewFIMDirWatcher(roots []string, ignore []string, baselinePath string, intervalSec, maxFiles, maxSizeKB int, logger module.Logger, emitFn func(module.SecurityEvent), key []byte) *FIMDirWatcher {
	if intervalSec < 5 {
		intervalSec = 10
	}
	if maxFiles < 16 {
		maxFiles = 4096
	}
	if maxSizeKB < 1 {
		maxSizeKB = 4096
	}
	f := &FIMDirWatcher{
		roots:        trimRoots(roots),
		ignore:       ignore,
		baselinePath: baselinePath,
		interval:     time.Duration(intervalSec) * time.Second,
		maxFiles:     maxFiles,
		maxSize:      int64(maxSizeKB) * 1024,
		logger:       logger,
		emitFn:       emitFn,
		key:          key,
		baseline:     make(map[string]fimDirEntry),
	}
	f.initialized = f.loadState()
	return f
}

// Interval is the fallback scan cadence. The primary path is event-driven; this
// is what covers an environment where inotify is unavailable.
func (f *FIMDirWatcher) Interval() time.Duration {
	if f == nil {
		return 0
	}
	return f.interval
}

// SetLang sets the language used for notifications.
func (f *FIMDirWatcher) SetLang(lang string) {
	if f == nil {
		return
	}
	f.langMu.Lock()
	f.language = lang
	f.langMu.Unlock()
}

func (f *FIMDirWatcher) lang() string {
	f.langMu.RLock()
	defer f.langMu.RUnlock()
	if f.language == "" {
		return "ja"
	}
	return f.language
}

// trimRoots normalises the configured roots (trailing slash, duplicates).
func trimRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	seen := make(map[string]bool, len(roots))
	for _, r := range roots {
		r = strings.TrimSuffix(strings.TrimSpace(r), "/")
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// Check hashes every file under the roots and reports the difference from the
// baseline. The first call records the baseline without emitting events.
func (f *FIMDirWatcher) Check() {
	if f == nil || len(f.roots) == 0 {
		return
	}
	f.scan(nil)
}

// HandleChanges is the event-driven entry point: inotify reported these paths.
// files are non-directory paths; dirs are paths the kernel marked IN_ISDIR and
// are expanded into a subtree scan. nil files means the changed paths are
// unknown (queue overflow) and everything is rescanned.
//
// ディレクトリを展開するのは、新しく作られたディレクトリの中にファイルが
// 置かれた場合、inotify の監視を張る前に作成が済んでいて作成イベントを
// 取りこぼすことがあるため（実測: mkdir 直後のシェルリダイレクト）。中を
// 走査すれば、監視の設置タイミングに依存せず検知できる。
func (f *FIMDirWatcher) HandleChanges(files, dirs []string) {
	if f == nil || len(f.roots) == 0 {
		return
	}
	if files == nil {
		f.scan(nil) // 何が変わったか特定できない → 全体を走査
		return
	}
	paths := make([]string, 0, len(files)+len(dirs))
	paths = append(paths, files...)
	for _, d := range dirs {
		paths = append(paths, f.expandDir(d)...)
	}
	if len(paths) == 0 {
		// ディレクトリの変化だけ（中にファイルが無い、削除された等）は
		// 通知する対象が無い。
		return
	}
	f.scan(paths)
}

// HandlePaths re-checks the given paths (files). nil means "rescan
// everything"; an empty slice is a no-op.
func (f *FIMDirWatcher) HandlePaths(paths []string) {
	if f == nil || len(f.roots) == 0 {
		return
	}
	if paths == nil {
		f.scan(nil)
		return
	}
	f.HandleChanges(paths, nil)
}

// expandDir returns the regular files under d (depth limited and filtered), so
// a file that appeared inside a fresh directory before the inotify watch was
// installed is still hashed. A missing d simply yields nothing.
func (f *FIMDirWatcher) expandDir(d string) []string {
	root := watchRootFor(f.roots, d)
	if root == "" || f.ignored(d) || depthRelative(root, d) > fimDirMaxDepth {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if f.ignored(p) {
				return fs.SkipDir
			}
			if depthRelative(root, p) > fimDirMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if f.ignored(p) || !e.Type().IsRegular() {
			return nil
		}
		out = append(out, p)
		return nil
	})
	return out
}

func (f *FIMDirWatcher) scan(paths []string) {
	events := f.scanLocked(paths)
	for _, ev := range events {
		if f.emitFn != nil {
			f.emitFn(ev)
		}
	}
}

func (f *FIMDirWatcher) scanLocked(paths []string) []module.SecurityEvent {
	f.mu.Lock()
	defer f.mu.Unlock()

	// 最初の走査は必ず全体を見て、静かにベースラインを記録する
	// （既存の数千ファイルを「作成」として通知しないため）。
	full := paths == nil || !f.initialized

	current := make(map[string]fimDirEntry)
	var requested []string
	scanned, oversized, unreadable := 0, 0, 0
	capped := false

	hashInto := func(p string) {
		if scanned >= f.maxFiles {
			capped = true
			return
		}
		st, err := os.Lstat(p)
		if err != nil {
			// 消えている・権限が無い → current に入れない（削除判定で扱う）。
			return
		}
		if !st.Mode().IsRegular() {
			// symlink / FIFO / socket はハッシュ対象にしない
			// （hashFile も O_NOFOLLOW で通常ファイル以外を拒否する）。
			return
		}
		if f.maxSize > 0 && st.Size() > f.maxSize {
			oversized++
			return
		}
		h, err := hashFile(p)
		if err != nil {
			unreadable++
			return
		}
		scanned++
		current[p] = fimDirEntry{Hash: h, Size: st.Size()}
	}

	if full {
		for _, root := range f.roots {
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if f.ignored(p) {
						return fs.SkipDir
					}
					if depthRelative(root, p) > fimDirMaxDepth {
						return fs.SkipDir
					}
					return nil
				}
				if f.ignored(p) || !d.Type().IsRegular() {
					return nil
				}
				hashInto(p)
				if capped {
					return fs.SkipAll
				}
				return nil
			})
		}
	} else {
		seen := make(map[string]bool, len(paths))
		for _, p := range paths {
			root := watchRootFor(f.roots, p)
			if root == "" || f.ignored(p) {
				continue // 設定されたルートの外は対象外
			}
			if depthRelative(root, p) > fimDirMaxDepth {
				continue
			}
			if seen[p] {
				continue // 同じパスが複数回通知されても 1 回だけ扱う
			}
			seen[p] = true
			requested = append(requested, p)
			hashInto(p)
		}
	}

	if !f.initialized {
		f.initialized = true
		f.baseline = current
		f.saveLocked()
		if f.logger != nil {
			f.logger.Info("Kizuna-Security FIM(ディレクトリ): ベースラインを記録しました (%d ファイル / 対象: %s)", len(current), strings.Join(f.roots, ","))
		}
		return nil
	}
	return f.diffLocked(full, capped, oversized, unreadable, current, requested)
}

// diffLocked compares current against the baseline, updates the baseline and
// returns the notifications. f.mu must be held.
func (f *FIMDirWatcher) diffLocked(full, capped bool, oversized, unreadable int, current map[string]fimDirEntry, requested []string) []module.SecurityEvent {
	var events []module.SecurityEvent
	suppressed := 0
	emit := func(level, titleKey, msgKey, source string, args ...interface{}) {
		if len(events) >= fimDirEventCap {
			suppressed++
			return
		}
		events = append(events, f.event(level, titleKey, msgKey, source, args...))
	}

	// 作成・変更（走査できたファイル）。
	for p, entry := range current {
		old, known := f.baseline[p]
		if !known {
			emit("critical", "fimwatch.create.title", "fimwatch.create.msg", p, p)
			continue
		}
		if old.Hash != entry.Hash {
			emit("critical", "fimwatch.change.title", "fimwatch.change.msg", p, p)
		}
	}

	if full {
		// 削除は全体走査のときだけ判定する。イベント走査は通知されたパス
		// しか見ていないので「current に無い＝消えた」とは言えない。また
		// 上限に達した走査では、見ていないだけのファイルを削除と誤判定
		// しないよう削除検知を行わない。
		if !capped {
			for p := range f.baseline {
				if _, ok := current[p]; ok {
					continue
				}
				if f.ignored(p) {
					continue
				}
				emit("warning", "fimwatch.delete.title", "fimwatch.delete.msg", p, p)
			}
		}
		f.baseline = current
	} else {
		// 通知されたパスだけを更新する。全部を置き換えると、見ていない
		// ファイルが消えたことになってしまう。
		for p, entry := range current {
			f.baseline[p] = entry
		}
		for _, p := range requested {
			if _, ok := current[p]; ok {
				continue
			}
			// ディレクトリは IN_ISDIR 側（expandDir の走査）で扱う。
			// 通常ファイルと同じ経路に流すと「作成直後に消えた短命な
			// ファイル」として誤報してしまう。
			if isDir(p) {
				continue
			}
			_, known := f.baseline[p]
			delete(f.baseline, p)
			if known {
				emit("warning", "fimwatch.delete.title", "fimwatch.delete.msg", p, p)
			} else {
				// inotify が知らせたパスが走査時には消えている＝作成〜削除が
				// 走査の合間に完結した。内容は取得できないが起きたことは事実
				// なので通知する（/tmp の正規の一時ファイルでも同じ形になる
				// ため warning とし、critical の洪水を避ける）。
				emit("warning", "fimwatch.transient.title", "fimwatch.transient.msg", p, p)
			}
		}
	}

	// 走査できなかった範囲がある場合、状態が変わった時だけ通知する
	// （毎回通知すると /tmp では履歴と通知が埋まる）。
	degraded := capped || oversized > 0 || unreadable > 0
	roots := strings.Join(f.roots, ",")
	if degraded != f.degraded {
		events = append(events, f.event("warning", "fimwatch.degraded.title", "fimwatch.degraded.msg",
			roots, roots, capped, oversized, unreadable))
	} else if degraded && f.logger != nil {
		f.logger.Debug("Kizuna-Security FIM(ディレクトリ): 一部を走査できません (上限到達=%v 大きすぎ=%d 読取不能=%d)", capped, oversized, unreadable)
	}
	f.degraded = degraded

	if suppressed > 0 {
		events = append(events, f.event("warning", "fimwatch.many.title", "fimwatch.many.msg",
			roots, len(events)+suppressed, fimDirEventCap, suppressed))
	}
	if len(events) > 0 {
		f.saveLocked()
	}
	return events
}

// event builds one notification.
func (f *FIMDirWatcher) event(level, titleKey, msgKey, source string, args ...interface{}) module.SecurityEvent {
	return module.SecurityEvent{
		Category:  "integrity",
		Level:     level,
		Title:     msg(f.lang(), titleKey),
		Message:   msg(f.lang(), msgKey, args...),
		Source:    source,
		Timestamp: time.Now(),
	}
}

// ignored reports whether p matches one of the configured glob patterns. Both
// the base name ("*.swp") and the full path ("/tmp/foo/*.log") are matched.
func (f *FIMDirWatcher) ignored(p string) bool {
	if len(f.ignore) == 0 {
		return false
	}
	base := filepath.Base(p)
	for _, pat := range f.ignore {
		if ok, err := filepath.Match(pat, base); err == nil && ok {
			return true
		}
		if ok, err := filepath.Match(pat, p); err == nil && ok {
			return true
		}
	}
	return false
}

// loadState loads the persisted baseline. A state file whose signature does not
// verify is rejected after a critical notification (rebuilding it silently
// would let an attacker reset the monitoring by editing the file).
func (f *FIMDirWatcher) loadState() bool {
	data, err := os.ReadFile(f.baselinePath)
	if err != nil {
		return false
	}
	var st fimDirState
	if json.Unmarshal(data, &st) != nil || st.Baseline == nil {
		return false
	}
	if !f.verifyStateSignature(st) {
		return false
	}
	f.baseline = make(map[string]fimDirEntry, len(st.Baseline))
	for p, entry := range st.Baseline {
		// 設定から外れたルートの記録は引き継がない（次回の全体走査で
		// 「削除」と誤報しないため）。
		if watchRootFor(f.roots, p) == "" {
			continue
		}
		f.baseline[p] = entry
	}
	return true
}

// verifyStateSignature checks the signature of the persisted state. 署名の
// 検証規則は FIM 本体のベースラインと同じ（鍵付きなら HMAC-SHA256、鍵なし
// なら SHA-256）。
func (f *FIMDirWatcher) verifyStateSignature(st fimDirState) bool {
	if st.Sig == "" {
		if len(f.key) > 0 {
			f.warnTamper("署名がありません")
			return false
		}
		return true
	}
	sig := st.Sig
	st.Sig = ""
	want := signJSON(f.key, st)
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		f.warnTamper("署名が一致しません")
		return false
	}
	return true
}

// warnTamper notifies that the directory baseline could not be trusted.
func (f *FIMDirWatcher) warnTamper(reason string) {
	if f.emitFn != nil {
		f.emitFn(module.SecurityEvent{
			Category:  "integrity",
			Level:     "critical",
			Title:     msg(f.lang(), "integrity.baseline.tamper.title"),
			Message:   msg(f.lang(), "integrity.baseline.tamper.msg", f.baselinePath, reason),
			Source:    f.baselinePath,
			Timestamp: time.Now(),
		})
	}
	if f.logger != nil {
		f.logger.Error("Kizuna-Security FIM(ディレクトリ): ベースライン署名の検証に失敗: %s", reason)
	}
}

// saveLocked writes the state atomically (f.mu must be held).
func (f *FIMDirWatcher) saveLocked() {
	st := fimDirState{Baseline: f.baseline, Roots: f.roots}
	sig := st
	sig.Sig = ""
	st.Sig = signJSON(f.key, sig)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(f.baselinePath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0700)
	}
	_ = fsutil.WriteFileAtomic(f.baselinePath, data, 0600)
}
