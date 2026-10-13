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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mouuff/go-rocket-update/pkg/provider"

	"Kizuna-Eye/pkg/logger"
)

// scriptTimeout bounds a single update-script run. The script rebuilds and
// restarts services, so it is allowed to be slow, but a hung build must not
// keep the updater goroutine blocked forever (and, on shutdown, delay exit).
const scriptTimeout = 30 * time.Minute

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

	mu          sync.Mutex
	lastCheck   time.Time
	lastVersion string
	lastErr     string
}

// New creates an updater. It returns nil when disabled or misconfigured.
func New(cfg Config, lg *logger.Logger) *Updater {
	if !cfg.Enabled {
		return nil
	}
	if cfg.RepositoryURL == "" || cfg.UpdateScript == "" {
		if lg != nil {
			lg.WarnT("updater.incomplete")
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
		u.lg.InfoT("updater.enabled", u.cfg.Interval, u.cfg.CurrentVersion)
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
			u.lg.WarnT("updater.check_failed", err)
		}
		return
	}

	u.mu.Lock()
	u.lastVersion = latest
	u.lastErr = ""
	u.mu.Unlock()

	if !isNewer(latest, u.cfg.CurrentVersion) {
		if u.lg != nil {
			u.lg.DebugT("updater.latest", u.cfg.CurrentVersion)
		}
		return
	}

	if u.lg != nil {
		u.lg.InfoT("updater.new_version", u.cfg.CurrentVersion, latest)
	}
	if err := u.runUpdateScript(); err != nil {
		u.mu.Lock()
		u.lastErr = err.Error()
		u.mu.Unlock()
		if u.lg != nil {
			u.lg.ErrorT("updater.script_failed", err)
		}
		return
	}
	if u.lg != nil {
		u.lg.InfoT("updater.script_done")
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

// runUpdateScript executes safe_update.sh after checking that the script is a
// trustworthy regular file.
//
// The script runs with the agent's privileges and performs git pull + build +
// service restart, so it is a code-execution boundary. Auto-update is off by
// default (design 10.3), but when an operator enables it the script itself
// must not be replaceable by a non-privileged writer: a world/group-writable
// update script in a shared directory (e.g. the Samba share) would let any
// local user swap it for their own and have the agent run it. The check is
// fail-closed and runs every time (not only at startup), so a later chmod or
// swap is caught before the next execution.
func (u *Updater) runUpdateScript() error {
	dir := scriptDir(u.cfg.UpdateScript)
	resolved, err := u.verifyUpdateScript()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
	defer cancel()

	// Anchor a bare name to its directory (cmd.Dir) with an explicit "./", so
	// exec.Command does not search $PATH (which would let a PATH entry win).
	script := resolved
	if !strings.ContainsAny(script, `/\`) {
		script = "./" + script
	}
	cmd := exec.CommandContext(ctx, script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if u.lg != nil && len(out) > 0 {
		u.lg.InfoT("updater.output", u.cfg.UpdateScript, string(out))
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("update script timed out after %s", scriptTimeout)
	}
	return err
}

// verifyUpdateScript resolves the update script and refuses to run it unless it
// is a regular file that only its owner can write. It also logs the path and
// SHA-256 for the audit trail, and returns the path to execute.
func (u *Updater) verifyUpdateScript() (string, error) {
	path := u.cfg.UpdateScript
	if path == "" {
		return "", fmt.Errorf("update script is not configured")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("update script path: %w", err)
	}

	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("update script not found (%s): %w", abs, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("update script is a symlink (refusing): %s", abs)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("update script is not a regular file: %s", abs)
	}
	// Fail closed on any write bit for group/other: the script is executed
	// with this process's privileges, so only the owner may change it.
	if info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("update script is group/other-writable (refusing): %s (%04o)", abs, info.Mode().Perm())
	}

	if u.lg != nil {
		if sum, serr := fileSHA256(abs); serr == nil {
			u.lg.InfoT("updater.script_verify", abs, sum)
		} else {
			u.lg.WarnT("updater.script_hash_failed", abs, serr)
		}
	}
	return abs, nil
}

// fileSHA256 returns the hex SHA-256 of path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Status returns a snapshot for the dashboard/API.
func (u *Updater) Status() map[string]interface{} {
	if u == nil {
		return map[string]interface{}{"enabled": false}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return map[string]interface{}{
		"enabled":      true,
		"current":      u.cfg.CurrentVersion,
		"latest":       u.lastVersion,
		"last_check":   u.lastCheck.Format(time.RFC3339),
		"last_error":   u.lastErr,
		"interval_sec": int(u.cfg.Interval.Seconds()),
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
	// Cap the number of components so a pathological version string cannot
	// make the comparison loop iterate over a huge slice.
	if len(parts) > 16 {
		parts = parts[:16]
	}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		// Cap each numeric component. Without this a version like
		// "99999999999999999999" overflows int and can become negative,
		// flipping the comparison (a "newer" version compares as older,
		// so the auto-update silently never fires). The cap is far above
		// any real version number.
		for _, r := range p {
			if r < '0' || r > '9' {
				break
			}
			if n > 1_000_000_000 {
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
