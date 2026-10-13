package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/module"
)

type CronMonitor struct {
	paths        []string
	baselinePath string
	lang         string
	logger       module.Logger
	emitFn       func(module.SecurityEvent)

	mu          sync.Mutex
	baseline    map[string]string
	watched     map[string]bool
	initialized bool

	// unreadable remembers watch directories that exist but cannot be
	// listed (e.g. /var/spool/cron/crontabs, mode drwx-wx--T). Changes
	// under such a path are invisible, so the operator is warned once
	// per path instead of the monitoring failing silently.
	unreadable map[string]bool
}

type cronState struct {
	Baseline map[string]string `json:"baseline"`
	Watched  []string          `json:"watched"`
}

func NewCronMonitor(paths []string, baselinePath string, lang string, logger module.Logger, emitFn func(module.SecurityEvent)) *CronMonitor {
	c := &CronMonitor{
		paths:        paths,
		baselinePath: baselinePath,
		lang:         lang,
		logger:       logger,
		emitFn:       emitFn,
		baseline:     make(map[string]string),
		watched:      make(map[string]bool),
		unreadable:   make(map[string]bool),
	}
	c.initialized = c.loadBaseline()
	return c
}

func (c *CronMonitor) Check() {
	if c == nil || len(c.paths) == 0 {
		return
	}

	// Warn if a watch path is unreadable before scanning, so a blind
	// spot (unreadable cron dir) is reported instead of silent success.
	c.checkUnreadable()

	current := c.scan()

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	if !c.initialized {
		c.initialized = true
		c.baseline = current
		for p := range current {
			c.watched[p] = true
		}
		c.saveLocked()
		if c.logger != nil {
			c.logger.Info("Kizuna-Security cron: ベースラインを記録しました (%d ファイル)", len(current))
		}
		return
	}

	var events []module.SecurityEvent
	for p, h := range current {
		old, existed := c.baseline[p]
		if !existed {
			// A file that appears under a watched directory (a new
			// /etc/cron.d entry, or a new user crontab) is a new cron
			// entry and must be announced. The old check only looked at
			// c.watched[p], which is false for a file seen for the very
			// first time, so such a file was absorbed into the baseline
			// silently and never reported.
			if !c.watched[p] && !c.underWatchedDir(p) {
				continue
			}
			ev := i18nEvent(c.lang, "cron", "critical", "cron.add.title", "cron.add.msg",
				module.SecurityEvent{
					Source:      p,
					Timestamp:   now,
					Command:     fmt.Sprintf("crontab -e (%s)", p),
					DetectFile:  "cronmon.go",
					DetectLine:  99,
					Remediation: fmt.Sprintf("cron ジョブが新規作成されました (%s)。心当たりが無ければ永続化を狙った攻撃の可能性があるため、内容を確認し不正なら crontab -r で削除してください。", p),
					RelatedLog:  fmt.Sprintf("cron: %s が新規に作成されました", p),
				}, p)
			events = append(events, ev)
			continue
		}
		if old != h {
			ev := i18nEvent(c.lang, "cron", "critical", "cron.change.title", "cron.change.msg",
				module.SecurityEvent{
					Source:      p,
					Timestamp:   now,
					Command:     fmt.Sprintf("crontab -e (%s)", p),
					DetectFile:  "cronmon.go",
					DetectLine:  110,
					Remediation: fmt.Sprintf("cron ジョブの内容が変更されました (%s)。至急内容を確認し、不正なジョブなら削除してください。", p),
					RelatedLog:  fmt.Sprintf("cron: %s が変更されました (hash %s -> %s)", p, old, h),
				}, p)
			events = append(events, ev)
		}
	}
	for p := range c.baseline {
		if !c.watched[p] {
			continue
		}
		if _, ok := current[p]; !ok {
			ev := i18nEvent(c.lang, "cron", "warning", "cron.delete.title", "cron.delete.msg",
				module.SecurityEvent{
					Source:      p,
					Timestamp:   now,
					Command:     fmt.Sprintf("crontab -r (%s)", p),
					DetectFile:  "cronmon.go",
					DetectLine:  125,
					Remediation: fmt.Sprintf("cron ジョブが削除されました (%s)。意図的な削除か、攻撃者が痕跡を消したかを確認してください。", p),
					RelatedLog:  fmt.Sprintf("cron: %s が削除されました", p),
				}, p)
			events = append(events, ev)
		}
	}

	c.baseline = current
	for p := range current {
		c.watched[p] = true
	}
	c.saveLocked()

	for _, ev := range events {
		c.emitFn(ev)
	}
}

func (c *CronMonitor) scan() map[string]string {
	current := make(map[string]string)
	for _, p := range c.paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if info.IsDir() {
			// The crontabs directory is mode drwx-wx--T (root:crontab). With
			// A-4 the agent holds CAP_DAC_READ_SEARCH (AmbientCapabilities)
			// and reads it directly; a non-A-4 deployment falls back to the
			// legacy sudo helper. If both fail, fall back to the (blind)
			// walk and the unreadable warning in checkUnreadable().
			if p == cronSudoDir {
				if entries, herr := readCronEntries(p); herr == nil {
					for name, h := range entries {
						current[filepath.Join(p, name)] = h
					}
					continue
				}
			}
			_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if h, herr := hashFile(path); herr == nil {
					current[path] = h
				}
				return nil
			})
			continue
		}
		if h, herr := hashFile(p); herr == nil {
			current[p] = h
		}
	}
	return current
}

func (c *CronMonitor) loadBaseline() bool {
	data, err := os.ReadFile(c.baselinePath)
	if err != nil {
		return false
	}
	var st cronState
	if json.Unmarshal(data, &st) != nil || st.Baseline == nil {
		return false
	}
	c.baseline = st.Baseline
	for _, p := range st.Watched {
		c.watched[p] = true
	}
	return true
}

func (c *CronMonitor) saveLocked() {
	for _, p := range c.paths {
		c.watched[p] = true
	}
	watched := make([]string, 0, len(c.watched))
	for p := range c.watched {
		watched = append(watched, p)
	}
	st := cronState{Baseline: c.baseline, Watched: watched}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(c.baselinePath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0700)
	}
	// fsutil: 一時ファイル→fsync→rename（L-12）。
	_ = fsutil.WriteFileAtomic(c.baselinePath, data, 0600)
}

// checkUnreadable emits a warning for each watch directory that exists
// but cannot be listed. Such a directory is a blind spot: changes under
// it (e.g. a new user crontab) are never detected. The warning is
// emitted once per path so it does not spam every scan cycle; if the
// path becomes readable again the flag is cleared.
func (c *CronMonitor) checkUnreadable() {
	if c == nil {
		return
	}
	for _, p := range c.paths {
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue
		}
		_, rerr := os.ReadDir(p)
		if rerr != nil {
			// For the crontabs directory a direct read (A-4 capability) or
			// the legacy sudo helper may still cover it, in which case
			// monitoring is not actually blind.
			if p == cronSudoDir {
				if _, herr := readCronEntries(p); herr == nil {
					if c.unreadable[p] {
						delete(c.unreadable, p)
					}
					continue
				}
			}
			if c.unreadable[p] {
				continue
			}
			c.unreadable[p] = true
			c.emitFn(i18nEvent(c.lang, "cron", "warning", "cron.unreadable.title", "cron.unreadable.msg",
				module.SecurityEvent{Source: p, Timestamp: time.Now()}, p))
			if c.logger != nil {
				c.logger.Warn("Kizuna-Security cron: 監視不能ディレクトリ: %s", p)
			}
		} else if c.unreadable[p] {
			delete(c.unreadable, p)
		}
	}
}

// cronSudoDir is the per-user crontab directory. It is mode drwx-wx--T
// (root:crontab) and cannot be listed by the agent user.
const cronSudoDir = "/var/spool/cron/crontabs"

// cronSudoHelper is the read-only helper allowed via sudoers
// (/etc/sudoers.d/kizuna-security-cron). It prints "<name>\t<sha256>" lines.
const cronSudoHelper = "/usr/local/bin/kizuna-cron-read.sh"

// readCronEntries returns base name -> sha256 for the per-user crontab
// directory. It tries a direct read first (the A-4 path: the agent holds
// CAP_DAC_READ_SEARCH and can list the otherwise unreadable drwx-wx--T dir),
// then falls back to the legacy sudo helper for a non-A-4 deployment. An error
// means neither worked and the caller should use the blind walk + unreadable
// warning.
func readCronEntries(dir string) (map[string]string, error) {
	if entries, err := readCronDirDirect(dir); err == nil {
		return entries, nil
	}
	return readCronViaSudo()
}

// readCronDirDirect lists dir with os.ReadDir and hashes each regular file.
// It requires read permission on the directory; without CAP_DAC_READ_SEARCH
// the drwx-wx--T crontabs directory is not listable and this returns an error.
func readCronDirDirect(dir string) (map[string]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
			continue
		}
		h, herr := hashFile(filepath.Join(dir, name))
		if herr != nil {
			continue
		}
		out[name] = h
	}
	return out, nil
}

// readCronViaSudo runs the read-only helper with sudo -n and parses its
// output. It returns base name -> sha256. Any error (helper missing, sudo
// denied, timeout) is returned so the caller can fall back to the blind walk
// and the unreadable warning. It is kept as a fallback for deployments where
// the agent is not yet A-4 (no CAP_DAC_READ_SEARCH).
func readCronViaSudo() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sudo", "-n", cronSudoHelper).Output()
	if err != nil {
		return nil, err
	}
	entries := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, h := parts[0], parts[1]
		if name == "" || h == "" {
			continue
		}
		// A helper must never be able to inject a path: only a plain file
		// name is accepted.
		if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
			continue
		}
		entries[name] = h
	}
	return entries, nil
}

// underWatchedDir reports whether p is a file directly under one of the
// configured watch directories.
func (c *CronMonitor) underWatchedDir(p string) bool {
	dir := filepath.Dir(p)
	for _, w := range c.paths {
		if w == dir {
			return true
		}
	}
	return false
}
