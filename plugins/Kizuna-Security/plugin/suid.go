package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/module"
)

const (
	maxSUIDEntries = 20000
	suidMaxDepth   = 5
)

// SUIDMonitor は SUID/SGID ビットが付いたファイルを検出する。
//
// SUID/SGID ファイルは権限昇格に悪用されやすい。既知の一覧をベースライン
// として保持し、新しく出現したものを「重大(critical)」として通知する。
// OS 更新で正当にモードが変わることもあるため、既知ファイルのモード変更は
// 「警戒(warning)」として通知する。スキャンは重いので間隔を空け、かつ
// 指定した時間帯（既定は深夜3時）に限定して低優先度で実行する。
type SUIDMonitor struct {
	paths        []string
	baselinePath string
	interval     time.Duration
	scanHour     int // 0-23, -1 = any hour
	lang         string
	logger       module.Logger
	emitFn       func(module.SecurityEvent)

	mu          sync.Mutex
	known       map[string]string // path -> octal mode (e.g. "4755")
	lastScan    time.Time
	scanning    bool
	initialized bool
	// hasBaseline is true once a baseline has been recorded (or loaded
	// from disk). len(known) must not be used for this: a system with zero
	// SUID files has an empty baseline, and that would make every new SUID
	// file look like "the first baseline" and be absorbed silently.
	hasBaseline bool
}

type suidState struct {
	Files map[string]string `json:"files"`
}

func NewSUIDMonitor(paths []string, baselinePath string, interval time.Duration, scanHour int, lang string, logger module.Logger, emitFn func(module.SecurityEvent)) *SUIDMonitor {
	return &SUIDMonitor{
		paths:        paths,
		baselinePath: baselinePath,
		interval:     interval,
		scanHour:     scanHour,
		lang:         lang,
		logger:       logger,
		emitFn:       emitFn,
		known:        make(map[string]string),
	}
}

// Check は間隔と時間帯の制限を守って走査する（定期ポーリング用）。
func (s *SUIDMonitor) Check() { s.check(false) }

// CheckForce は間隔の制限を無視して即座に走査する。inotify のように
// 「ツリーが変化した」ことが確実なイベント駆動の呼び出しで使う。時間帯の
// 制限は守る（時間帯限定の監視はその枠の中でだけ動かすため）。
func (s *SUIDMonitor) CheckForce() { s.check(true) }

// Interval は走査間隔を返す。イベント駆動監視のフォールバック用ポーリングが
// 同じ間隔を使えるように公開する。
func (s *SUIDMonitor) Interval() time.Duration {
	if s == nil {
		return 0
	}
	return s.interval
}

func (s *SUIDMonitor) check(force bool) {
	if s == nil || len(s.paths) == 0 {
		return
	}

	s.mu.Lock()
	// 指定時間帯以外は走査しない（低スペック機の負荷を避ける）。
	if s.scanHour >= 0 && time.Now().Hour() != s.scanHour {
		s.mu.Unlock()
		return
	}
	if !force && !s.lastScan.IsZero() && time.Since(s.lastScan) < s.interval {
		s.mu.Unlock()
		return
	}
	// 走査は数秒かかることがある。定期ポーリングとイベント駆動が重なったとき
	// に同じ差分を二重に処理しないよう、実行中は後続をスキップする。
	// （イベントは失われない: 走査は必ず最新のツリー全体を見るため、実行中の
	// 変化は次回の走査で拾える。）
	if s.scanning {
		s.mu.Unlock()
		return
	}
	s.lastScan = time.Now()
	s.scanning = true
	// パニック時も走査中フラグを必ず戻す（フラグが立ったままだと以後の走査が
	// すべてスキップされ、監視が黙って止まる）。
	defer func() {
		s.mu.Lock()
		s.scanning = false
		s.mu.Unlock()
	}()
	if !s.initialized {
		s.initialized = true
		s.loadBaselineLocked()
	}
	hasBaseline := s.hasBaseline
	baseline := make(map[string]string, len(s.known))
	for k, v := range s.known {
		baseline[k] = v
	}
	s.mu.Unlock()

	current := s.scan()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !hasBaseline {
		s.known = current
		s.hasBaseline = true
		s.saveBaselineLocked()
		if s.logger != nil {
			s.logger.Info("Kizuna-Security SUID: ベースラインを記録しました (%d 件)", len(current))
		}
		return
	}

	now := time.Now()
	var events []module.SecurityEvent
	for path, mode := range current {
		old, existed := baseline[path]
		if !existed {
			label := "SUID"
			if strings.HasPrefix(mode, "2") || strings.HasPrefix(mode, "6") {
				label = "SGID"
			}
			ev := i18nEvent(s.lang, "suid", "critical", "suid.new.title", "suid.new.msg",
				module.SecurityEvent{
					Source:      path,
					Timestamp:   now,
					Command:     fmt.Sprintf("chmod %s %s", mode, path),
					DetectFile:  "suid.go",
					DetectLine:  152,
					Remediation: fmt.Sprintf("%s ビット付きファイル %s は権限昇格に悪用されます。心当たりが無ければ即削除し、設置者・設置時刻 (ls -l --time-style=full-iso %s) と親プロセスを調査してください。正規のソフトでも SUID は最小限に絞るべきです。", label, path, path),
					RelatedLog:  fmt.Sprintf("%s mode=%s", path, mode),
				}, label)
			events = append(events, ev)
		} else if old != mode {
			ev := i18nEvent(s.lang, "suid", "warning", "suid.mode.title", "suid.mode.msg",
				module.SecurityEvent{
					Source:      path,
					Timestamp:   now,
					DetectFile:  "suid.go",
					DetectLine:  161,
					Remediation: fmt.Sprintf("既存ファイルのモードが %s から %s に変化しました。意図した変更か確認し、心当たりが無ければ元のモードへ戻してください。", old, mode),
					RelatedLog:  fmt.Sprintf("%s mode %s -> %s", path, old, mode),
				}, path, old, mode)
			events = append(events, ev)
		}
	}

	s.known = current
	s.saveBaselineLocked()

	for _, ev := range events {
		s.emitFn(ev)
	}
}

// scan walks the configured paths and returns path -> octal mode for every
// regular file that has the SUID or SGID bit set. The walk runs at a lowered
// process priority so it does not starve other work on a low-power host.
func (s *SUIDMonitor) scan() map[string]string {
	restore := lowerPriority()
	defer restore()

	current := make(map[string]string)
	count := 0
	for _, root := range s.paths {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if count >= maxSUIDEntries {
				return filepath.SkipAll
			}
			if d.IsDir() {
				rel, rerr := filepath.Rel(root, path)
				if rerr == nil && rel != "." {
					if strings.Count(rel, string(os.PathSeparator))+1 > suidMaxDepth {
						return filepath.SkipDir
					}
				}
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil || !info.Mode().IsRegular() {
				return nil
			}
			mode := info.Mode()
			if mode&(os.ModeSetuid|os.ModeSetgid) == 0 {
				return nil
			}
			current[path] = octalMode(mode)
			count++
			return nil
		})
	}
	return current
}

// lowerPriority lowers the process nice value for the duration of a heavy scan
// and restores it afterwards. It is best-effort: if it fails, the scan still
// runs. This affects the whole agent process only while the scan runs, which
// keeps the host responsive on low-power hardware.
func lowerPriority() (restore func()) {
	orig, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	if err != nil {
		return func() {}
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19)
	return func() { _ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, orig) }
}

// octalMode formats a file mode as a 4-digit octal string (e.g. "4755").
func octalMode(m os.FileMode) string {
	special := 0
	if m&os.ModeSetuid != 0 {
		special += 4
	}
	if m&os.ModeSetgid != 0 {
		special += 2
	}
	if m&os.ModeSticky != 0 {
		special += 1
	}
	return fmt.Sprintf("%d%03o", special, m.Perm())
}

func (s *SUIDMonitor) loadBaselineLocked() {
	data, err := os.ReadFile(s.baselinePath)
	if err != nil {
		return
	}
	var st suidState
	if err := json.Unmarshal(data, &st); err != nil {
		return
	}
	for k, v := range st.Files {
		s.known[k] = v
	}
	s.hasBaseline = true
}

func (s *SUIDMonitor) saveBaselineLocked() {
	if s.baselinePath == "" {
		return
	}
	data, err := json.MarshalIndent(suidState{Files: s.known}, "", "  ")
	if err != nil {
		return
	}
	// fsutil: 一時ファイル→fsync→rename（L-12: 9箇所に重複していた処理を
	// pkg/fsutil に共通化）。親ディレクトリ作成も fsutil が行う。
	_ = fsutil.WriteFileAtomic(s.baselinePath, data, 0600)
}
