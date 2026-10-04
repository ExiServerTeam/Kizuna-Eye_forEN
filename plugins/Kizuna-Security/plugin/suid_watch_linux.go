//go:build linux

package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// suidWatchMask is the set of events that can produce a new SUID/SGID file:
//
//   - IN_CREATE / IN_MOVED_TO: a file (or directory) appeared (cp, mv, rename)
//   - IN_ATTRIB:               chmod u+s on an existing file
//   - IN_CLOSE_WRITE:          a written file was closed
//
// Deletions are watched too so a removed file is dropped from the watch table
// (the baseline itself is rebuilt on every scan, so the detection does not
// depend on these events). IN_ONLYDIR keeps a file from being watched by
// mistake. IN_ONESHOT is deliberately not used: the watch must survive.
const suidWatchMask = syscall.IN_CREATE | syscall.IN_MOVED_TO | syscall.IN_ATTRIB |
	syscall.IN_CLOSE_WRITE | syscall.IN_DELETE | syscall.IN_MOVED_FROM |
	syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF | syscall.IN_ONLYDIR

// start installs the watches and starts the reader. An error means inotify is
// unavailable; the caller then relies on the periodic scan alone.
func (w *suidWatcher) start() error {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return fmt.Errorf("inotify_init1: %w", err)
	}

	w.mu.Lock()
	w.fd = fd
	w.running = true
	w.mu.Unlock()

	for _, p := range w.paths {
		if isDir(p) {
			w.addTree(p)
			continue
		}
		// 設定されたルートがまだ存在しない場合は、最も近い既存の親を監視する。
		// 親で作成イベントを受けると handleEvents が addTree(新ディレクトリ) を
		// 呼び、本来のルート以下に監視が張られる（攻撃者が最初にディレクトリを
		// 作る形の攻撃を、周期走査の間隔に依存せず捉えるため）。
		if dir := nearestExistingDir(p); dir != "" {
			w.addWatch(dir)
		}
	}

	if w.logger != nil {
		w.mu.Lock()
		n, capped := len(w.wdToPath), w.capped
		w.mu.Unlock()
		if capped {
			// 監視本数の上限はホスト全体の予算（systemd やエディタと共有）なので、
			// 巨大なツリーを丸ごと監視せず、はみ出した分は周期走査に任せる。
			w.logger.Warn("Kizuna-Security %s: inotify の監視上限 (%d) に達しました。%d 個のディレクトリを即時監視し、残りは定期走査で検知します。", w.label, suidWatchMaxWatches, n)
		} else {
			w.logger.Info("Kizuna-Security %s: %d 個のディレクトリを inotify で即時監視します（対象: %s）", w.label, n, strings.Join(w.paths, ","))
		}
	}

	go w.debounceLoop()
	go w.readLoop()
	return nil
}

// stop releases the inotify fd and stops the reader/debouncer goroutines.
func (w *suidWatcher) stop() {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return
	}
	w.closing = true
	fd := w.fd
	w.fd = -1
	w.running = false
	w.wdToPath = make(map[int]string)
	w.mu.Unlock()

	close(w.done)
	if fd >= 0 {
		_ = syscall.Close(fd)
	}
}

// disable marks the watch unusable so the plugin falls back to periodic scans
// and an operator sees why.
func (w *suidWatcher) disable(reason string, err error) {
	w.mu.Lock()
	closing := w.closing
	w.running = false
	w.mu.Unlock()
	if !closing && w.logger != nil {
		w.logger.Warn("Kizuna-Security %s: inotify の%s (%v)。定期走査で検知を続けます。", w.label, reason, err)
	}
}

func (w *suidWatcher) readLoop() {
	buf := make([]byte, 64*1024)
	for {
		select {
		case <-w.done:
			return
		default:
		}
		w.mu.Lock()
		fd := w.fd
		w.mu.Unlock()
		if fd < 0 {
			return
		}
		n, err := syscall.Read(fd, buf)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EINTR {
				select {
				case <-w.done:
					return
				case <-time.After(suidWatchPollDelay):
				}
				continue
			}
			w.disable("読み取りが失敗しました", err)
			return
		}
		if n > 0 {
			w.handleEvents(buf[:n])
		}
	}
}

// handleEvents walks the inotify buffer, keeps the watch table in sync with the
// directories that come and go, and raises one debounced notification.
func (w *suidWatcher) handleEvents(buf []byte) {
	changed := false
	for off := 0; off+syscall.SizeofInotifyEvent <= len(buf); {
		raw := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
		nameLen := int(raw.Len)
		name := ""
		if nameLen > 0 && off+syscall.SizeofInotifyEvent+nameLen <= len(buf) {
			b := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+nameLen]
			name = strings.TrimRight(string(b), "\x00")
		}
		off += syscall.SizeofInotifyEvent + nameLen

		mask := raw.Mask
		if mask&syscall.IN_Q_OVERFLOW != 0 {
			// キュー溢れ: 何を取りこぼしたか特定できないので全体を走査し直す。
			if w.logger != nil {
				w.logger.Warn("Kizuna-Security %s: inotify のイベントキューが溢れました。取りこぼしを取り戻すため全走査します。", w.label)
			}
			w.mu.Lock()
			w.overflow = true
			w.mu.Unlock()
			changed = true
			continue
		}

		wd := int(raw.Wd)
		if mask&syscall.IN_IGNORED != 0 {
			// カーネルが監視を外した（ディレクトリ削除など）。
			w.mu.Lock()
			delete(w.wdToPath, wd)
			w.mu.Unlock()
			continue
		}

		w.mu.Lock()
		dir, ok := w.wdToPath[wd]
		w.mu.Unlock()
		if !ok {
			continue
		}
		full := dir
		if name != "" {
			full = filepath.Join(dir, name)
		}
		// 攻撃者は mkdir してからバイナリを置く。新しいディレクトリは中も監視する。
		if mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 && isDir(full) {
			w.addTree(full)
		}
		// 変化したパスを記録する。FIM ディレクトリ監視は「どのファイルが
		// 変わったか」を使い、そのファイルだけをその場でハッシュする
		// （全体走査を避けるため）。カーネルが IN_ISDIR を付けたパスは
		// ディレクトリとして渡す（FIM 側は中を走査し、通常ファイルとして
		// 「作成直後に消えた」と誤報しない）。上限を超えた分は記録しないが、
		// パスが分からないだけで周期走査の対象からは外れない。
		if name != "" {
			w.mu.Lock()
			if mask&syscall.IN_ISDIR != 0 {
				if len(w.pendingDirs) < suidWatchMaxPending {
					w.pendingDirs[full] = true
				}
			} else if len(w.pending) < suidWatchMaxPending {
				w.pending[full] = true
			}
			w.mu.Unlock()
		}
		changed = true
	}
	if changed {
		w.notify()
	}
}

// addTree installs watches for root and its subdirectories down to
// suidWatchMaxDepth. Called for the configured roots at start-up and for
// directories created while the watch is running.
func (w *suidWatcher) addTree(root string) {
	base := watchRootFor(w.paths, root)
	if base == "" {
		return
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		depth := depthRelative(base, p)
		if depth < 0 || depth > suidWatchMaxDepth {
			return fs.SkipDir
		}
		w.addWatch(p)
		return nil
	})
}

func (w *suidWatcher) addWatch(dir string) {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return
	}
	if len(w.wdToPath) >= suidWatchMaxWatches {
		w.capped = true
		w.mu.Unlock()
		return
	}
	fd := w.fd
	w.mu.Unlock()
	if fd < 0 {
		return
	}
	wd, err := syscall.InotifyAddWatch(fd, dir, suidWatchMask)
	if err != nil {
		return
	}
	w.mu.Lock()
	if !w.closing {
		w.wdToPath[wd] = dir
	}
	w.mu.Unlock()
}
