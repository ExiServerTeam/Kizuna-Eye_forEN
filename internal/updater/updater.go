// Package updater implements a version check against GitHub Releases and,
// when a newer version is available, delegates the actual update to the
// safe_update.sh script.
//
// Why not use go-rocket-update's Updater.Update() directly?
// Kizuna-Eye ships two binaries (agent_linux, dashboard_linux) plus plugin
// .so files, and Go plugins require an exact hash match with the host. A
// single-binary self-update would replace only one file and break plugin
// loading ("plugin was built with a different version of package ...").
// So we use go-rocket-update only to *detect* the latest version, and let
// safe_update.sh perform a consistent, rollback-capable update of everything.
package updater

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mouuff/go-rocket-update/pkg/provider"

	"Kizuna-Eye/pkg/logger"
)

// Config controls the auto-update loop.
type Config struct {
	// Enabled turns the periodic check on.
	Enabled bool
	// RepositoryURL is the GitHub repository, e.g. "github.com/owner/repo".
	RepositoryURL string
	// ArchiveName is the release asset name used by go-rocket-update to
	// resolve releases (e.g. "kizuna-eye-linux-amd64.tar.gz"). It must match
	// what you upload to GitHub Releases.
	ArchiveName string
	// UpdateScript is the path to safe_update.sh.
	UpdateScript string
	// Interval is how often to check for a new version.
	Interval time.Duration
	// CurrentVersion is the running version (e.g. internal/api.Version).
	CurrentVersion string
}

// Updater periodically checks GitHub Releases and runs the update script.
type Updater struct {
	cfg Config
	lg  *logger.Logger

	mu              sync.Mutex
	lastCheck       time.Time
	lastVersion     string
	lastErr         string
}

// New creates an updater. It returns nil when disabled or misconfigured.
func New(cfg Config, lg *logger.Logger) *Updater {
	if !cfg.Enabled {
		return nil
	}
	if cfg.RepositoryURL == "" || cfg.UpdateScript == "" {
		if lg != nil {
			lg.Warn("自動更新: 設定が不完全です（repository_url / update_script）")
		}
		return nil
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 6 * time.Hour
	}
	return &Updater{cfg: cfg, lg: lg}
}

// Run starts the periodic check loop. It blocks until stop is closed.
func (u *Updater) Run(stop <-chan struct{}) {
	if u == nil {
		return
	}
	if u.lg != nil {
		u.lg.Info("自動更新: 有効（確認間隔: %s, 現在: %s）", u.cfg.Interval, u.cfg.CurrentVersion)
	}

	// Initial check shortly after startup, then on every tick.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-stop:
			return
		case <-timer.C:
			u.checkAndUpdate()
			timer.Reset(u.cfg.Interval)
		}
	}
}

// checkAndUpdate queries the latest version and, if newer, runs the script.
func (u *Updater) checkAndUpdate() {
	u.mu.Lock()
	u.lastCheck = time.Now()
	u.mu.Unlock()

	latest, err := u.latestVersion()
	if err != nil {
		u.mu.Lock()
		u.lastErr = err.Error()
		u.mu.Unlock()
		if u.lg != nil {
			u.lg.Warn("自動更新: バージョン確認失敗: %v", err)
		}
		return
	}

	u.mu.Lock()
	u.lastVersion = latest
	u.lastErr = ""
	u.mu.Unlock()

	if !isNewer(latest, u.cfg.CurrentVersion) {
		if u.lg != nil {
			u.lg.Debug("自動更新: 最新版です (%s)", u.cfg.CurrentVersion)
		}
		return
	}

	if u.lg != nil {
		u.lg.Info("自動更新: 新バージョン検知 %s → %s。更新スクリプトを実行します", u.cfg.CurrentVersion, latest)
	}
	if err := u.runUpdateScript(); err != nil {
		u.mu.Lock()
		u.lastErr = err.Error()
		u.mu.Unlock()
		if u.lg != nil {
			u.lg.Error("自動更新: スクリプト実行失敗: %v", err)
		}
		return
	}
	if u.lg != nil {
		u.lg.Info("自動更新: 更新スクリプト完了（次回起動で反映されます）")
	}
}

// latestVersion fetches the latest release tag from GitHub.
func (u *Updater) latestVersion() (string, error) {
	p := &provider.Github{
		RepositoryURL: u.cfg.RepositoryURL,
		ArchiveName:   u.cfg.ArchiveName,
	}
	if err := p.Open(); err != nil {
		return "", fmt.Errorf("provider open: %w", err)
	}
	defer p.Close()
	return p.GetLatestVersion()
}

// runUpdateScript executes safe_update.sh and streams nothing (output is
// inherited so it lands in the agent/dashboard log).
func (u *Updater) runUpdateScript() error {
	cmd := exec.Command(u.cfg.UpdateScript)
	cmd.Dir = scriptDir(u.cfg.UpdateScript)
	out, err := cmd.CombinedOutput()
	if u.lg != nil && len(out) > 0 {
		u.lg.Info("自動更新: %s 出力:\n%s", u.cfg.UpdateScript, string(out))
	}
	return err
}

// Status returns a snapshot for the dashboard/API.
func (u *Updater) Status() map[string]interface{} {
	if u == nil {
		return map[string]interface{}{"enabled": false}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return map[string]interface{}{
		"enabled":        true,
		"current":        u.cfg.CurrentVersion,
		"latest":         u.lastVersion,
		"last_check":     u.lastCheck.Format(time.RFC3339),
		"last_error":     u.lastErr,
		"interval_sec":   int(u.cfg.Interval.Seconds()),
	}
}

// isNewer reports whether latest is newer than current using a simple
// dotted-version comparison (e.g. v0.6.2 > v0.6.1). It ignores a leading "v".
func isNewer(latest, current string) bool {
	l := parseVersion(latest)
	c := parseVersion(current)
	n := len(l)
	if len(c) > n {
		n = len(c)
	}
	for i := 0; i < n; i++ {
		var lv, cv int
		if i < len(l) {
			lv = l[i]
		}
		if i < len(c) {
			cv = c[i]
		}
		if lv != cv {
			return lv > cv
		}
	}
	return false
}

func parseVersion(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	// Drop pre-release/build metadata (e.g. 1.2.3-rc1+meta).
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}

func scriptDir(path string) string {
	if idx := strings.LastIndexAny(path, "/\\"); idx > 0 {
		return path[:idx]
	}
	return "."
}
