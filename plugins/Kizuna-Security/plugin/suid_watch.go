package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/module"
)

// 攻撃者は「SUID を付けたファイルを数秒だけ置いて消す」ことができる。
// 周期走査では、走査と走査の間に作成〜削除が完結したファイルを原理的に
// 見られない（周期 15 秒・存在窓 10 秒なら約 1/3 を取りこぼす）。
// その窓を閉じるのが inotify によるイベント駆動の検知で、ディレクトリの
// 変化（作成・改名・属性変更）を契機にその場で SUID 走査を回す。
//
// 監視本数には上限がある（fs.inotify.max_user_watches は systemd や
// エディタと共有するホスト全体の予算）。/home のような巨大なツリーを
// 丸ごと監視すると予算を食い潰すため、監視は「動きの速い置き場」
// (suid_watch_paths, 既定 /tmp,/var/tmp,/dev/shm,/run) に限定し、
// それ以外は従来どおり周期走査 (suid_fast_paths) でカバーする。
const (
	// suidWatchMaxDepth limits how deep below a watched root the watcher
	// installs directory watches automatically. Deeper trees stay covered by the
	// periodic scan; the limit keeps the number of kernel watches bounded.
	suidWatchMaxDepth = 3
	// suidWatchMaxWatches caps the inotify watches this plugin adds, so a large
	// tree cannot exhaust the host-wide limit and break other software.
	suidWatchMaxWatches = 2048
	// suidWatchDebounceDelay coalesces the burst of events one drop produces
	// (IN_CREATE + IN_ATTRIB + IN_CLOSE_WRITE) into a single scan.
	suidWatchDebounceDelay = 300 * time.Millisecond
	// suidWatchPollDelay is how often the reader checks for shutdown while the
	// inotify fd reports EAGAIN.
	suidWatchPollDelay = 100 * time.Millisecond
	// suidWatchMaxPending bounds how many changed paths are remembered between
	// two notifications. A directory storm (rm -rf on a huge tree) would
	// otherwise grow the set without limit; the excess only costs precision
	// (those paths fall back to the periodic scan).
	suidWatchMaxPending = 4096
)

// suidWatcher turns "a directory under a watched path changed" into an immediate
// scan.
//
// The type is the generic inotify layer of this plugin: callers differ only in
// the label used for log lines and in what they do with the notification. The
// SUID monitor only needs "something changed" (onChange wrapping), while the
// FIM directory watch needs the changed paths so it can hash those files
// instead of rescanning the whole tree.
type suidWatcher struct {
	paths []string
	// label names the feature in log messages ("SUID" / "FIM").
	label  string
	logger module.Logger
	// onChange receives the changed paths. files are non-directory paths; dirs
	// are paths the kernel marked IN_ISDIR (created/renamed/removed directories),
	// which the FIM directory watch expands into a subtree scan. A nil files
	// slice means "the changed paths could not be determined" (inotify queue
	// overflow): the caller must rescan everything.
	onChange func(files, dirs []string)

	mu       sync.Mutex
	fd       int
	running  bool
	closing  bool
	wdToPath map[int]string
	capped   bool
	// maxDepth is how deep below a root directory watches are installed
	// (0 = the root directory only).
	maxDepth int
	// maxDirsPerRoot caps the watches installed per root. dirCapped records the
	// roots that already reported the cap so the warning is emitted once.
	maxDirsPerRoot int
	dirCapped      map[string]bool
	// dirCapFn runs once per root when the per-root cap is reached, so the FIM
	// watch can turn a silently truncated watch set into an operator alert.
	dirCapFn func(root string, limit int)
	// pending accumulates the paths reported since the last debounce.
	pending map[string]bool
	// pendingDirs is the subset the kernel reported as directories (IN_ISDIR).
	// ディレクトリを通常ファイルと同じ扱いにすると「作成直後に消えた短命な
	// ファイル」として誤報し、逆に中に置かれたファイルを取りこぼす。
	pendingDirs map[string]bool
	// overflow records that the inotify queue overflowed, so at least one
	// change was lost and the changed paths are unknown.
	overflow bool

	// trigger carries "something changed" to the debouncer. It is buffered and
	// only ever written non-blocking, so a busy directory cannot stall the reader.
	trigger chan struct{}
	done    chan struct{}
}

// inotifyOption customises the generic inotify watcher (watch depth, per-root
// watch cap). Callers that pass none get the defaults used by the SUID watch.
type inotifyOption func(*suidWatcher)

// withMaxDepth limits how deep below a root directory watches are installed.
// depth 0 watches the root directory only.
func withMaxDepth(depth int) inotifyOption {
	return func(w *suidWatcher) {
		if depth >= 0 {
			w.maxDepth = depth
		}
	}
}

// withMaxDirsPerRoot caps the watches installed per root. onCap runs once per
// root after the cap is reached, so the caller can notify the operator that the
// rest of the tree is only covered by the periodic scan.
func withMaxDirsPerRoot(limit int, onCap func(root string, limit int)) inotifyOption {
	return func(w *suidWatcher) {
		if limit > 0 {
			w.maxDirsPerRoot = limit
		}
		w.dirCapFn = onCap
	}
}

// newInotifyWatcher builds the generic inotify watcher. onChange receives the
// changed paths (files/dirs; nil files = unknown, rescan everything).
func newInotifyWatcher(paths []string, label string, logger module.Logger, onChange func(files, dirs []string), opts ...inotifyOption) *suidWatcher {
	if label == "" {
		label = "SUID"
	}
	w := &suidWatcher{
		paths:          paths,
		label:          label,
		logger:         logger,
		onChange:       onChange,
		maxDepth:       suidWatchMaxDepth,
		maxDirsPerRoot: suidWatchMaxWatches,
		fd:             -1,
		wdToPath:       make(map[int]string),
		dirCapped:      make(map[string]bool),
		pending:        make(map[string]bool),
		pendingDirs:    make(map[string]bool),
		trigger:        make(chan struct{}, 1),
		done:           make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}
	return w
}

// countRootLocked returns how many watches are installed under root.
// w.mu must be held.
func (w *suidWatcher) countRootLocked(root string) int {
	if root == "" {
		return 0
	}
	n := 0
	for _, p := range w.wdToPath {
		if p == root || strings.HasPrefix(p, root+"/") {
			n++
		}
	}
	return n
}

func newSUIDWatcher(paths []string, logger module.Logger, onChange func()) *suidWatcher {
	var cb func([]string, []string)
	if onChange != nil {
		cb = func([]string, []string) { onChange() }
	}
	return newInotifyWatcher(paths, "SUID", logger, cb)
}

// takePending returns the paths seen since the last call and resets the sets.
// A nil files slice means the queue overflowed: the caller must rescan
// everything (the dirs are meaningless then).
func (w *suidWatcher) takePending() ([]string, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overflow {
		w.overflow = false
		w.pending = make(map[string]bool)
		w.pendingDirs = make(map[string]bool)
		return nil, nil
	}
	files := make([]string, 0, len(w.pending))
	for p := range w.pending {
		files = append(files, p)
	}
	dirs := make([]string, 0, len(w.pendingDirs))
	for p := range w.pendingDirs {
		dirs = append(dirs, p)
	}
	w.pending = make(map[string]bool)
	w.pendingDirs = make(map[string]bool)
	return files, dirs
}

// Running reports whether the event-driven watch is active. Callers use it to
// know whether the watched paths are covered in real time or only by polling.
func (w *suidWatcher) Running() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// notify queues a scan.
func (w *suidWatcher) notify() {
	select {
	case w.trigger <- struct{}{}:
	default:
	}
}

// debounceLoop collapses a burst of notifications into a single scan a short
// while after the storm settles, then runs the caller's scan.
func (w *suidWatcher) debounceLoop() {
	for {
		select {
		case <-w.done:
			return
		case <-w.trigger:
		}
		timer := time.NewTimer(suidWatchDebounceDelay)
		waiting := true
		for waiting {
			select {
			case <-w.done:
				timer.Stop()
				return
			case <-w.trigger:
				// 連続イベントは締め切りを延ばす（cp と chmod は続けて来る）。
				timer.Reset(suidWatchDebounceDelay)
			case <-timer.C:
				waiting = false
			}
		}
		if w.onChange != nil {
			// 変化したパスを渡す（nil は「特定できない」＝全体を走査）。
			// SUID 監視は引数を無視し、FIM ディレクトリ監視はそのパスだけを
			// その場でハッシュする。
			w.onChange(w.takePending())
		}
	}
}

// depthRelative returns how many components below root p sits (0 = root itself),
// or -1 when p is not below root.
func depthRelative(root, p string) int {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return -1
	}
	if rel == "." {
		return 0
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return -1
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

// watchRootFor returns the configured root p belongs to ("" when none).
func watchRootFor(roots []string, p string) string {
	for _, r := range roots {
		r = strings.TrimSuffix(r, "/")
		if r == "" {
			continue
		}
		if p == r || strings.HasPrefix(p, r+"/") {
			return r
		}
	}
	return ""
}

// isDir reports whether p is a directory (events can refer to files too).
func isDir(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.IsDir()
}

// nearestExistingDir returns p when it is a directory, otherwise its closest
// existing ancestor ("" when there is none).
//
// 設定された監視ルートがまだ存在しない場合（例: /tmp/incoming を攻撃者が最初に
// 作る）は、そのルートを監視できない。最も近い既存の親を監視しておけば、
// ルートが作られた時点で IN_CREATE を受け取り、その中以下の監視を張れる
// （存在しないルートを作る攻撃を、周期走査の間隔に依存せず捉えるため）。
func nearestExistingDir(p string) string {
	for {
		if isDir(p) {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p || parent == "" || parent == "." {
			return ""
		}
		p = parent
	}
}
