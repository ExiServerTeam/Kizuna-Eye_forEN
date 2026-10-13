// updater パッケージは GitHub Releases に対するバージョンチェックを実装し、
// 新しいバージョンがあれば実際の更新を safe_update.sh スクリプトへ委譲する。
//
// なぜ go-rocket-update の Updater.Update() を直接使わないか?
// Kizuna-Eye は 2 つのバイナリ（agent_linux、dashboard_linux）とプラグイン .so を
// 同梱し、Go プラグインはホストとの完全なハッシュ一致を要求する。単一バイナリの
// 自己更新は 1 ファイルだけを置き換え、プラグインのロードを壊す
// （"plugin was built with a different version of package ..."）。
// そのため go-rocket-update は最新バージョンの「検出」にのみ使い、一貫性があり
// ロールバック可能な全更新は safe_update.sh に任せる。
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

// scriptTimeout は 1 回の更新スクリプト実行を制限する。スクリプトは再ビルドと
// サービスの再起動を行うため遅くてもよいが、ハングしたビルドが updater ゴルーチンを
// 永遠にブロックしてはならない（そしてシャットダウン時に終了を遅らせてもならない）。
// scriptTimeout bounds a single update-script run. The script rebuilds and
// restarts services, so it is allowed to be slow, but a hung build must not
// keep the updater goroutine blocked forever (and, on shutdown, delay exit).
const scriptTimeout = 30 * time.Minute

// Config は自動更新ループを制御する。
// Config controls the auto-update loop.
type Config struct {
	// Enabled は定期チェックをオンにする。
	// Enabled turns the periodic check on.
	Enabled bool
	// RepositoryURL は GitHub リポジトリ（例: "github.com/owner/repo"）。
	// RepositoryURL is the GitHub repository, e.g. "github.com/owner/repo".
	RepositoryURL string
	// ArchiveName は go-rocket-update がリリースを解決するのに使うリリースアセット名
	// （例: "kizuna-eye-linux-amd64.tar.gz"）。GitHub Releases へアップロードする
	// ものと一致させなければならない。
	// ArchiveName is the release asset name used by go-rocket-update to
	// resolve releases (e.g. "kizuna-eye-linux-amd64.tar.gz"). It must match
	// what you upload to GitHub Releases.
	ArchiveName string
	// UpdateScript は safe_update.sh へのパスだ。
	// UpdateScript is the path to safe_update.sh.
	UpdateScript string
	// Interval は新バージョンをチェックする間隔だ。
	// Interval is how often to check for a new version.
	Interval time.Duration
	// CurrentVersion は実行中のバージョンだ（例: internal/api.Version）。
	// CurrentVersion is the running version (e.g. internal/api.Version).
	CurrentVersion string
}

// Updater は定期的に GitHub Releases をチェックし、更新スクリプトを実行する。
// Updater periodically checks GitHub Releases and runs the update script.
type Updater struct {
	cfg Config
	lg  *logger.Logger

	mu          sync.Mutex
	lastCheck   time.Time
	lastVersion string
	lastErr     string
}

// New は updater を作る。無効または設定不備の場合は nil を返す。
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

// Run は定期チェックループを開始する。stop が閉じられるまでブロックする。
// Run starts the periodic check loop. It blocks until stop is closed.
func (u *Updater) Run(stop <-chan struct{}) {
	if u == nil {
		return
	}
	if u.lg != nil {
		u.lg.InfoT("updater.enabled", u.cfg.Interval, u.cfg.CurrentVersion)
	}

	// 起動直後に初回チェックし、その後は毎 tick で。
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

// checkAndUpdate は最新バージョンを問い合わせ、新しければスクリプトを実行する。
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

// latestVersion は GitHub から最新リリースタグを取得する。
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

// runUpdateScript はスクリプトが信頼できる通常ファイルであることを確認した上で
// safe_update.sh を実行する。
//
// スクリプトは agent の権限で実行され、git pull + ビルド + サービス再起動を行うため、
// コード実行境界だ。自動更新は既定で無効だが（設計 10.3）、運用者が有効化したとき、
// スクリプト自体が非特権の書き手に置き換えられてはならない。共有ディレクトリ
// （例: Samba 共有）にある world/group 書き込み可能な更新スクリプトは、任意の
// ローカルユーザーが自分のものに差し替え、agent にそれを実行させられる。
// 確認は fail-closed で、毎回実行する（起動時のみではない）。これにより後日の
// chmod や差し替えが次の実行前に捕まる。
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

	// 素の名前をそのディレクトリ（cmd.Dir）に明示的な "./" で固定し、
	// exec.Command が $PATH を検索しないようにする（PATH エントリが勝ち得るため）。
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

// verifyUpdateScript は更新スクリプトを解決し、所有者のみが書き込める通常ファイルで
// なければ実行を拒否する。監査証跡用にパスと SHA-256 もログし、実行するパスを返す。
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
	// group/other の書き込みビットがあれば fail-closed にする。スクリプトは
	// このプロセスの権限で実行されるため、所有者のみが変更できる。
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

// fileSHA256 は path の 16 進 SHA-256 を返す。
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

// Status は dashboard/API 用のスナップショットを返す。
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

// isNewer は latest が current より新しいかを、単純なドットバージョン比較で返す
// （例: v0.6.2 > v0.6.1）。先頭の "v" は無視する。
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
	// プレリリース/ビルドメタデータ（例: 1.2.3-rc1+meta）を落とす。
	// Drop pre-release/build metadata (e.g. 1.2.3-rc1+meta).
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	// コンポーネント数を制限し、病的なバージョン文字列が比較ループを巨大な
	// スライスで反復させないようにする。
	// Cap the number of components so a pathological version string cannot
	// make the comparison loop iterate over a huge slice.
	if len(parts) > 16 {
		parts = parts[:16]
	}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		// 各数値コンポーネントを制限する。これが無いと
		// "99999999999999999999" のようなバージョンが int をオーバーフローして
		// 負になり得て、比較が反転する（"新しい" バージョンが古いと比較され、
		// 自動更新が黙って決して発火しなくなる）。上限は実在のどのバージョン
		// 番号よりはるかに大きい。
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
