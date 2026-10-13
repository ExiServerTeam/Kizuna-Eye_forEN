package main

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// SecurityConfig はセキュリティプラグインの設定。
type SecurityConfig struct {
	LogPath       string   // このプラグイン自身の検知ログ（JSON Lines）
	WatchFiles    []string // 監視するログファイル（auth.log など）
	PollInterval  int      // 監視間隔（秒）
	FailedBurst   int      // 同一IPからの失敗がこの回数に達したら重大とする
	BurstWindow   int      // 失敗回数を数える時間枠（秒）
	NotifyMinimal string   // 通知する最小レベル（critical / warning / info）
	// Language selects the notification language ("ja" | "en"). Notifications
	// are emitted server-side, so this is explicit rather than derived from the
	// browser locale.
	Language string

	// SSH ログイン成功の監視
	SSHLoginBurst        int    // 同一IPからのログイン成功がこの回数に達したら急増とみなす
	SSHLoginWindow       int    // 成功回数を数える時間枠（秒）
	SSHLoginBaselinePath string // 既知IPの保存先（未知IP判定用）
	// SSHLoginAllowlist lists IPs whose ssh_login events are suppressed
	// entirely (task 7). A monitoring host or the operator's workstation
	// reconnects often and would otherwise fire the "login burst" critical
	// and pollute the login counters. Empty by default.
	SSHLoginAllowlist []string

	// SSH ログイン失敗の検知強化（2026-10-04。攻撃 A-4「SSH 失敗連続」の回帰）。
	//
	// 失敗回数は「最初の失敗から BurstWindow 以内」ではなくスライディング窓で
	// 数える。これだけでは 1〜2 分に1回の低頻度攻撃を捉えられないため、
	// 長窓の総数（SSHFailedSustainedBurst / Window）と、ユーザー名を回しながら
	// の列挙（SSHEnumDistinctUsers / Window）を別ルールの critical とする。
	SSHFailedSustainedBurst  int // 長窓内の失敗回数がこれに達したら critical
	SSHFailedSustainedWindow int // 失敗を数える長窓（秒）
	SSHEnumDistinctUsers     int // 同一送信元が試した異なるユーザー名がこれに達したら critical
	SSHEnumWindow            int // ユーザー名の種類を数える窓（秒）

	// ファイル完全性監視 (FIM)
	IntegrityFiles        []string // 改ざんを監視する重要ファイル
	IntegrityBaselinePath string   // ベースラインの保存先

	// FIM ディレクトリ監視（inotify。2026-10-04 の攻撃 A-5 の回帰）。
	//
	// IntegrityFiles は「絶対パスを列挙してハッシュ比較する」方式なので、
	// 攻撃者が任意の名前でファイルを作る置き場（/tmp 等）は監視できない。
	// 実機テストでは /tmp/kizuna-fim-test/watched.txt の作成→変更→削除が、
	// FIM の走査が 2 回入っていたにもかかわらず 1 件も検知されなかった。
	//
	// FIMWatch を有効にすると FIMWatchPaths 配下を inotify で監視し、
	// 作成・変更・削除（および走査前に消えた短命なファイル）を検知する。
	// 検知の主経路はイベント駆動で、FIMWatchInterval は inotify が使えない
	// 環境・通知を取りこぼした場合の再走査間隔。
	//
	// 既定は無効。ビジーな /tmp を丸ごと監視すると正規の一時ファイルでも
	// critical が出るため、監視対象を絞って（または FIMWatchIgnore と併用して）
	// 有効化する運用を想定している。
	FIMWatch             bool
	FIMWatchPaths        []string // 監視するディレクトリ
	FIMWatchBaselinePath string   // ディレクトリ監視のベースライン
	FIMWatchInterval     int      // フォールバック走査の間隔（秒）
	FIMWatchMaxFiles     int      // 1回の走査でハッシュする最大ファイル数
	FIMWatchMaxSizeKB    int      // これより大きいファイルはハッシュしない（KB）
	// FIMWatchMaxDepth is how deep below each root directory watches are
	// installed. Files in a directory deeper than this stay outside the covered
	// range, so the limit is reported to the operator instead of being silent.
	FIMWatchMaxDepth int
	// FIMWatchMaxDirs caps the inotify watches installed per root so a large
	// tree cannot exhaust the host-wide inotify budget.
	FIMWatchMaxDirs int
	// FIMWatchHeadBytes is how many leading bytes of an oversized file are
	// hashed (0 = disabled, max 1 MiB). A rewritten head is then detectable
	// even when the whole file is too large to hash (task 5).
	FIMWatchHeadBytes int
	FIMWatchIgnore    []string // 除外する glob（例: *.swp）

	// 新規リッスンポート検知
	ListenPortCheck        bool
	ListenPortBaselinePath string
	// ListenPortRenotifyCooldownSec suppresses re-notifying the same port
	// that is closed and reopened within this many seconds (task 8). An
	// attacker (or a restarting service) that flaps one port would otherwise
	// produce a notification per flap. 0 disables it (old behaviour).
	ListenPortRenotifyCooldownSec int

	// SUID/SGID 検知
	SUIDCheck        bool
	SUIDPaths        []string
	SUIDBaselinePath string
	SUIDScanInterval int // スキャン間隔（秒）
	// SUIDScanHour restricts the scan to a single hour of the day (0-23) so a
	// low-power host is not loaded during business hours. -1 disables the window.
	SUIDScanHour int

	// SUIDFastCheck は「攻撃者が実際に置く場所」を短い間隔で走査する第二の
	// SUID 監視を有効にする。
	//
	// 既定の SUIDPaths は /usr 以下のみで、しかも SUIDScanHour（既定3時）に
	// 1時間間隔でしか動かない。そのため /tmp や /home に SUID バイナリを
	// 置かれても、時間帯とパスの両方の条件で一度も検知できなかった。
	// SUIDFastPaths は時間帯制限なし（scanHour=-1）で走査する。走査は
	// エージェントのポーリング間隔（既定15秒）に縛られない専用ループで回す。
	// ただし周期走査だけでは「走査と走査の間に作成〜削除が完結したファイル」を
	// 原理的に見られないため、動きの速い置き場は SUIDWatch（inotify）で
	// ディレクトリの変化を契機に即時走査する。SUIDFastScanInterval はその
	// イベント駆動が使えないときのフォールバック周期でもある。
	SUIDFastCheck        bool
	SUIDFastPaths        []string
	SUIDFastBaselinePath string
	SUIDFastScanInterval int // 秒

	// SUIDWatch は inotify によるイベント駆動の検知を有効にする。
	// SUIDWatchPaths は「動きが速く、攻撃者が実際に SUID を置く」置き場だけを
	// 指定する（/tmp 等）。/home のような巨大なツリーを丸ごと監視すると
	// fs.inotify.max_user_watches（ホスト全体の予算）を食い潰すため、
	// 監視はここに挙げたパスに限定し、それ以外は周期走査でカバーする。
	SUIDWatch      bool
	SUIDWatchPaths []string

	// cron 変更検知
	CronCheck        bool
	CronPaths        []string
	CronBaselinePath string

	// 自動ブロック (fail2ban 相当)
	BlockMode        string   // "off" | "dry-run" | "enforce"
	BlockDurationSec int      // ブロックを自動解除するまでの秒数
	BlockWhitelist   []string // 絶対にブロックしない IP / CIDR
	// BlockStatePath persists active blocks so a plugin restart does not lose
	// track of firewall rules that remain in the kernel.
	BlockStatePath string
	// FirewallBackend selects the blocking backend: "auto" | "iptables" | "nftables" | "firewalld".
	FirewallBackend string
	// SudoIgnoreCommands are command prefixes excluded from the sudo alert.
	SudoIgnoreCommands []string

	// V1-A: HMAC key for the security-log hash chain. Empty keeps the
	// legacy keyless chain.
	ChainKeyPath string
	// V1-A/V2-B: how often (seconds) to verify the chain / alert history.
	IntegrityCheckInterval int
	// V2-B: alert_history.jsonl consistency (written by the dashboard).
	AlertHistoryPath      string
	AlertHistoryStatePath string
	// V2-C: dedicated HMAC key used by the dashboard to sign each alert
	// history line. It must be a separate key from ChainKeyPath (one key per
	// use). Empty disables per-line signature verification (legacy lines and
	// unsigned history are skipped, so this is backward compatible).
	AlertHistoryKeyPath string
}

func DefaultConfig() *SecurityConfig {
	return &SecurityConfig{
		LogPath:       "./logs/kizuna-security.log",
		WatchFiles:    []string{"/var/log/auth.log", "/var/log/dpkg.log", "/var/log/apt/history.log"},
		PollInterval:  15,
		FailedBurst:   5,
		BurstWindow:   60,
		NotifyMinimal: "warning",
		Language:      "ja",

		SSHLoginBurst:        10,
		SSHLoginWindow:       300,
		SSHLoginBaselinePath: "./logs/kizuna-security-logins.json",
		SSHLoginAllowlist:    nil,

		// 短窓の burst を避けるために間隔を空けた低頻度攻撃（1〜2分に1回）は、
		// 5/60秒では永久に検知できない。長窓の総数とユーザー名の種類で補う。
		SSHFailedSustainedBurst:  15,
		SSHFailedSustainedWindow: 600,
		SSHEnumDistinctUsers:     5,
		SSHEnumWindow:            300,

		IntegrityFiles: []string{
			"/etc/passwd",
			"/etc/shadow",
			"/etc/group",
			"/etc/sudoers",
			"/etc/ssh/sshd_config",
			"/etc/ld.so.preload",
			"/etc/crontab",
			"/etc/hosts",
			"/root/.ssh/authorized_keys",
		},
		IntegrityBaselinePath: "./logs/kizuna-security-fim.json",

		// ディレクトリ監視は既定で無効（FIMWatch=false）。有効化したときに
		// 使う既定の対象は「攻撃者がものを置く置き場」だが、/tmp 全体は
		// 正規の一時ファイルでも通知が出るため、運用では対象を絞るか
		// fim_watch_ignore で除外することを想定している。
		FIMWatch:             false,
		FIMWatchPaths:        []string{"/tmp", "/var/tmp", "/dev/shm"},
		FIMWatchBaselinePath: "./logs/kizuna-security-fim-dirs.json",
		FIMWatchInterval:     10,
		FIMWatchMaxFiles:     4096,
		FIMWatchMaxSizeKB:    4096,
		FIMWatchMaxDepth:     3,
		FIMWatchMaxDirs:      1024,
		// FIMWatchHeadBytes is how many leading bytes of an oversized file are
		// hashed (0 = disabled, max 1 MiB). Default 64 KiB.
		FIMWatchHeadBytes: 64 * 1024,
		// 既定の除外パターン。エディタのバックアップ・ロックファイル、
		// systemd/snap の一時ディレクトリなど、正常な動作でも頻繁に
		// 作成/変更されるものを除外し、本当に見るべき改ざんが履歴と
		// 通知のノイズに埋もれるのを防ぐ。運用で追加したい場合は
		// modules.json の fim_watch_ignore で上書きする（既定を置換、
		// マージではない）。
		FIMWatchIgnore: []string{
			"#*", "*.swp", "*.swo", "*.swx", // エディタの作業ファイル
			"*.tmp", "*.temp", "*.bak", "*.old", // 一般的な一時/バックアップ
			".~lock.*",              // LibreOffice のロック
			"systemd-private-*",     // systemd の PrivateTmp
			"snap-private-tmp",      // snap の PrivateTmp
			".X*-lock", ".X11-unix", // X11 ソケット
			// データベース/ミドルウェアの正規の共有メモリ・セマフォ。
			// /dev/shm を監視するとこれらが短命ファイルとして毎秒通知され、
			// 本当の改ざんが埋もれる（実測: PostgreSQL.* が多発）。
			"PostgreSQL.*", "sem.*", "dbus-*", "snap.*",
			"*systemd-*", "gnome-*", "pulse-*", "X11-unix",
		},

		ListenPortCheck:               true,
		ListenPortBaselinePath:        "./logs/kizuna-security-ports.json",
		ListenPortRenotifyCooldownSec: 600,

		SUIDCheck:        true,
		SUIDPaths:        []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin", "/usr/local/bin", "/usr/local/sbin"},
		SUIDBaselinePath: "./logs/kizuna-security-suid.json",
		SUIDScanInterval: 3600,
		SUIDScanHour:     3,

		// 攻撃者が実際に SUID/SGID バイナリを置く場所（書き込み可能・noexec で
		// ない場所）を短い間隔で走査する。走査は nice/ionice で低優先度、
		// 深さ制限つきなので数千ファイル程度なら 1 秒未満で終わる。
		SUIDFastCheck:        true,
		SUIDFastPaths:        []string{"/tmp", "/var/tmp", "/dev/shm", "/run", "/home", "/opt", "/srv"},
		SUIDFastBaselinePath: "./logs/kizuna-security-suid-fast.json",
		SUIDFastScanInterval: 10,
		SUIDWatch:            true,
		SUIDWatchPaths:       []string{"/tmp", "/var/tmp", "/dev/shm", "/run"},

		CronCheck:        true,
		CronPaths:        []string{"/etc/crontab", "/etc/cron.d", "/etc/cron.daily", "/etc/cron.hourly", "/etc/cron.weekly", "/etc/cron.monthly", "/var/spool/cron", "/var/spool/cron/crontabs"},
		CronBaselinePath: "./logs/kizuna-security-cron.json",

		BlockMode:        "off",
		BlockDurationSec: 600,
		BlockWhitelist:   nil,
		BlockStatePath:   "./logs/kizuna-security-blocks.json",
		FirewallBackend:  "auto",
		// 自作の読み取り専用ヘルパー（cronSudoHelper）はポーリングごとに sudo で
		// 実行されるため既定で通知対象外にする（monitor.go は設定に関わらず
		// 常にこのヘルパーを無視する）。
		SudoIgnoreCommands: []string{cronSudoHelper, "/usr/sbin/smartctl", "smartctl"},
		// These defaults are relative to the agent's working directory, like
		// the other state files above (./logs/...). Absolute paths used to be
		// hardcoded here for one specific deployment, which made a fresh
		// install create the chain key outside the install directory.
		// install.sh / migrate-agent-user.sh / the .deb write their real
		// (absolute) paths into modules.json.
		ChainKeyPath:           "./keys/chain.key",
		IntegrityCheckInterval: 300,
		AlertHistoryPath:       "./logs/alert_history.jsonl",
		AlertHistoryStatePath:  "./logs/kizuna-security-alertstate.json",
		AlertHistoryKeyPath:    "./keys/alert_history.key",
	}
}

func ParseConfig(raw map[string]interface{}) (*SecurityConfig, error) {
	c := DefaultConfig()

	if v, ok := raw["log_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.LogPath = v
	}
	if v, ok := raw["watch_files"].(string); ok {
		c.WatchFiles = splitList(v)
	}
	if v, ok := raw["watch_files"].([]interface{}); ok {
		if list := toStringList(v); len(list) > 0 {
			c.WatchFiles = list
		}
	}
	if v, ok := raw["poll_interval_sec"].(float64); ok {
		c.PollInterval = int(v)
	}
	if v, ok := raw["failed_burst"].(float64); ok {
		c.FailedBurst = int(v)
	}
	if v, ok := raw["burst_window_sec"].(float64); ok {
		c.BurstWindow = int(v)
	}
	if v, ok := raw["notify_min_level"].(string); ok && v != "" {
		c.NotifyMinimal = strings.ToLower(v)
	}
	if v, ok := raw["language"].(string); ok && v != "" {
		c.Language = strings.ToLower(v)
	}

	if v, ok := raw["ssh_login_burst"].(float64); ok {
		c.SSHLoginBurst = int(v)
	}
	if v, ok := raw["ssh_login_window_sec"].(float64); ok {
		c.SSHLoginWindow = int(v)
	}
	if v, ok := raw["ssh_login_baseline_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.SSHLoginBaselinePath = v
	}
	if v, ok := raw["ssh_login_allowlist"].(string); ok {
		c.SSHLoginAllowlist = splitList(v)
	}
	if v, ok := raw["ssh_login_allowlist"].([]interface{}); ok {
		c.SSHLoginAllowlist = toStringList(v)
	}
	if v, ok := raw["failed_sustained_burst"].(float64); ok {
		c.SSHFailedSustainedBurst = int(v)
	}
	if v, ok := raw["failed_sustained_window_sec"].(float64); ok {
		c.SSHFailedSustainedWindow = int(v)
	}
	if v, ok := raw["ssh_enum_distinct_users"].(float64); ok {
		c.SSHEnumDistinctUsers = int(v)
	}
	if v, ok := raw["ssh_enum_window_sec"].(float64); ok {
		c.SSHEnumWindow = int(v)
	}

	if v, ok := raw["integrity_files"].(string); ok {
		c.IntegrityFiles = splitList(v)
	}
	if v, ok := raw["integrity_files"].([]interface{}); ok {
		c.IntegrityFiles = toStringList(v)
	}
	if v, ok := raw["integrity_baseline_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.IntegrityBaselinePath = v
	}
	if v, ok := toBool(raw["fim_watch"]); ok {
		c.FIMWatch = v
	}
	if v, ok := raw["fim_watch_paths"].(string); ok {
		c.FIMWatchPaths = splitList(v)
	}
	if v, ok := raw["fim_watch_paths"].([]interface{}); ok {
		c.FIMWatchPaths = toStringList(v)
	}
	if v, ok := raw["fim_watch_baseline_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.FIMWatchBaselinePath = v
	}
	if v, ok := raw["fim_watch_interval_sec"].(float64); ok {
		c.FIMWatchInterval = int(v)
	}
	if v, ok := raw["fim_watch_max_files"].(float64); ok {
		c.FIMWatchMaxFiles = int(v)
	}
	if v, ok := raw["fim_watch_max_size_kb"].(float64); ok {
		c.FIMWatchMaxSizeKB = int(v)
	}
	if v, ok := raw["fim_watch_max_depth"].(float64); ok {
		c.FIMWatchMaxDepth = int(v)
	}
	if v, ok := raw["fim_watch_max_dirs"].(float64); ok {
		c.FIMWatchMaxDirs = int(v)
	}
	if v, ok := raw["fim_watch_head_bytes"].(float64); ok {
		c.FIMWatchHeadBytes = int(v)
	}
	if v, ok := raw["fim_watch_ignore"].(string); ok {
		c.FIMWatchIgnore = splitList(v)
	}
	if v, ok := raw["fim_watch_ignore"].([]interface{}); ok {
		c.FIMWatchIgnore = toStringList(v)
	}

	if v, ok := toBool(raw["listen_port_check"]); ok {
		c.ListenPortCheck = v
	}
	if v, ok := raw["listen_port_baseline_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.ListenPortBaselinePath = v
	}
	if v, ok := raw["listen_port_renotify_cooldown_sec"].(float64); ok {
		c.ListenPortRenotifyCooldownSec = int(v)
	}

	if v, ok := toBool(raw["suid_check"]); ok {
		c.SUIDCheck = v
	}
	if v, ok := raw["suid_paths"].(string); ok {
		c.SUIDPaths = splitList(v)
	}
	if v, ok := raw["suid_paths"].([]interface{}); ok {
		c.SUIDPaths = toStringList(v)
	}
	if v, ok := raw["suid_baseline_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.SUIDBaselinePath = v
	}
	if v, ok := raw["suid_scan_interval_sec"].(float64); ok {
		c.SUIDScanInterval = int(v)
	}
	if v, ok := raw["suid_scan_hour"].(float64); ok {
		c.SUIDScanHour = int(v)
	}
	if v, ok := toBool(raw["suid_fast_check"]); ok {
		c.SUIDFastCheck = v
	}
	if v, ok := raw["suid_fast_paths"].(string); ok {
		c.SUIDFastPaths = splitList(v)
	}
	if v, ok := raw["suid_fast_paths"].([]interface{}); ok {
		c.SUIDFastPaths = toStringList(v)
	}
	if v, ok := raw["suid_fast_baseline_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.SUIDFastBaselinePath = v
	}
	if v, ok := raw["suid_fast_interval_sec"].(float64); ok {
		c.SUIDFastScanInterval = int(v)
	}
	if v, ok := toBool(raw["suid_watch"]); ok {
		c.SUIDWatch = v
	}
	if v, ok := raw["suid_watch_paths"].(string); ok {
		c.SUIDWatchPaths = splitList(v)
	}
	if v, ok := raw["suid_watch_paths"].([]interface{}); ok {
		c.SUIDWatchPaths = toStringList(v)
	}

	if v, ok := toBool(raw["cron_check"]); ok {
		c.CronCheck = v
	}
	if v, ok := raw["cron_paths"].(string); ok {
		c.CronPaths = splitList(v)
	}
	if v, ok := raw["cron_paths"].([]interface{}); ok {
		c.CronPaths = toStringList(v)
	}
	if v, ok := raw["cron_baseline_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.CronBaselinePath = v
	}

	if v, ok := raw["block_mode"].(string); ok && v != "" {
		c.BlockMode = strings.ToLower(v)
	}
	if v, ok := raw["block_duration_sec"].(float64); ok {
		c.BlockDurationSec = int(v)
	}
	if v, ok := raw["block_whitelist"].(string); ok {
		c.BlockWhitelist = splitList(v)
	}
	if v, ok := raw["block_whitelist"].([]interface{}); ok {
		c.BlockWhitelist = toStringList(v)
	}
	if v, ok := raw["block_state_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.BlockStatePath = v
	}
	if v, ok := raw["firewall_backend"].(string); ok && v != "" {
		c.FirewallBackend = strings.ToLower(v)
	}
	if v, ok := raw["sudo_ignore_commands"].(string); ok {
		c.SudoIgnoreCommands = splitList(v)
	}
	if v, ok := raw["sudo_ignore_commands"].([]interface{}); ok {
		c.SudoIgnoreCommands = toStringList(v)
	}
	if v, ok := raw["chain_key_path"].(string); ok {
		c.ChainKeyPath = v
	}
	if v, ok := raw["integrity_check_interval_sec"].(float64); ok {
		c.IntegrityCheckInterval = int(v)
	}
	if v, ok := raw["alert_history_key_path"].(string); ok {
		c.AlertHistoryKeyPath = v
	}
	if v, ok := raw["alert_history_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.AlertHistoryPath = v
	}
	if v, ok := raw["alert_history_state_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.AlertHistoryStatePath = v
	}

	return c, nil
}

// toBool accepts a JSON bool or a "true"/"false"-style string (the GUI form
// may submit either), returning false when the value is not recognised.
func toBool(v interface{}) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1", "on", "yes":
			return true, true
		case "false", "0", "off", "no":
			return false, true
		}
	}
	return false, false
}

func toStringList(v []interface{}) []string {
	list := make([]string, 0, len(v))
	for _, item := range v {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			list = append(list, strings.TrimSpace(s))
		}
	}
	return list
}

func (c *SecurityConfig) Validate() error {
	if len(c.WatchFiles) == 0 {
		return fmt.Errorf("watch_files は1つ以上指定してください")
	}
	if len(c.WatchFiles) > 64 {
		return fmt.Errorf("watch_files は64件以内で指定してください: %d", len(c.WatchFiles))
	}
	if c.PollInterval < 5 || c.PollInterval > 3600 {
		return fmt.Errorf("poll_interval_sec は 5〜3600 の範囲で指定してください: %d", c.PollInterval)
	}
	if c.FailedBurst < 1 {
		c.FailedBurst = 5
	}
	if c.FailedBurst > 100000 {
		c.FailedBurst = 100000
	}
	if c.BurstWindow < 5 {
		c.BurstWindow = 60
	}
	if c.BurstWindow > 86400 {
		c.BurstWindow = 86400
	}
	switch c.NotifyMinimal {
	case "critical", "warning", "info":
	default:
		c.NotifyMinimal = "warning"
	}
	switch c.Language {
	case "ja", "en":
	default:
		c.Language = "ja"
	}

	if c.SSHLoginBurst < 1 {
		c.SSHLoginBurst = 10
	}
	if c.SSHLoginBurst > 100000 {
		c.SSHLoginBurst = 100000
	}
	if c.SSHLoginWindow < 5 {
		c.SSHLoginWindow = 300
	}
	if c.SSHLoginWindow > 86400 {
		c.SSHLoginWindow = 86400
	}
	if len(c.SSHLoginAllowlist) > 64 {
		return fmt.Errorf("ssh_login_allowlist は64件以内で指定してください: %d", len(c.SSHLoginAllowlist))
	}

	if c.SSHFailedSustainedBurst < 1 {
		c.SSHFailedSustainedBurst = 15
	}
	if c.SSHFailedSustainedBurst > 100000 {
		c.SSHFailedSustainedBurst = 100000
	}
	// 短窓より狭い長窓は意味がないので、短窓以上へ持ち上げる。
	if c.SSHFailedSustainedWindow < c.BurstWindow {
		c.SSHFailedSustainedWindow = c.BurstWindow
	}
	if c.SSHFailedSustainedWindow > 86400 {
		c.SSHFailedSustainedWindow = 86400
	}
	if c.SSHEnumDistinctUsers < 2 {
		c.SSHEnumDistinctUsers = 5
	}
	if c.SSHEnumDistinctUsers > 100000 {
		c.SSHEnumDistinctUsers = 100000
	}
	if c.SSHEnumWindow < 5 {
		c.SSHEnumWindow = 300
	}
	if c.SSHEnumWindow > 86400 {
		c.SSHEnumWindow = 86400
	}

	// 監視対象は絶対パスの .log ファイル（または RHEL 系の secure / messages）のみ許可する。
	for i, wf := range c.WatchFiles {
		if err := validateWatchPath(wf); err != nil {
			return fmt.Errorf("watch_files[%d]: %w", i, err)
		}
	}
	if err := validateOutputPath(c.LogPath); err != nil {
		return fmt.Errorf("log_path: %w", err)
	}

	// FIM の監視対象は絶対パスのみ（拡張子不問）。
	if len(c.IntegrityFiles) > 256 {
		return fmt.Errorf("integrity_files は256件以内で指定してください: %d", len(c.IntegrityFiles))
	}
	for i, p := range c.IntegrityFiles {
		if err := sanitizePath(p); err != nil {
			return fmt.Errorf("integrity_files[%d]: %w", i, err)
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("integrity_files[%d]: 絶対パスで指定してください: %s", i, p)
		}
	}
	if c.IntegrityBaselinePath != "" {
		if err := sanitizePath(c.IntegrityBaselinePath); err != nil {
			return fmt.Errorf("integrity_baseline_path: %w", err)
		}
	}

	// FIM ディレクトリ監視。監視対象は絶対パスのディレクトリのみ許可する。
	if len(c.FIMWatchPaths) == 0 {
		c.FIMWatchPaths = []string{"/tmp", "/var/tmp", "/dev/shm"}
	}
	if len(c.FIMWatchPaths) > 64 {
		return fmt.Errorf("fim_watch_paths は64件以内で指定してください: %d", len(c.FIMWatchPaths))
	}
	for i, p := range c.FIMWatchPaths {
		if err := sanitizePath(p); err != nil {
			return fmt.Errorf("fim_watch_paths[%d]: %w", i, err)
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("fim_watch_paths[%d]: 絶対パスで指定してください: %s", i, p)
		}
	}
	if c.FIMWatchBaselinePath != "" {
		if err := sanitizePath(c.FIMWatchBaselinePath); err != nil {
			return fmt.Errorf("fim_watch_baseline_path: %w", err)
		}
	}
	if c.FIMWatchInterval < 5 {
		c.FIMWatchInterval = 10
	}
	if c.FIMWatchInterval > 3600 {
		c.FIMWatchInterval = 3600
	}
	if c.FIMWatchMaxFiles < 16 {
		c.FIMWatchMaxFiles = 4096
	}
	if c.FIMWatchMaxFiles > 1000000 {
		c.FIMWatchMaxFiles = 1000000
	}
	if c.FIMWatchMaxSizeKB < 1 {
		c.FIMWatchMaxSizeKB = 4096
	}
	if c.FIMWatchMaxSizeKB > 1048576 {
		c.FIMWatchMaxSizeKB = 1048576
	}
	// 深さ 0 は「ルート直下のみ」で有効。上限は inotify の watch 予算を
	// 食い潰さないための安全弁。
	if c.FIMWatchMaxDepth < 0 || c.FIMWatchMaxDepth > 16 {
		c.FIMWatchMaxDepth = 3
	}
	if c.FIMWatchMaxDirs < 16 || c.FIMWatchMaxDirs > 200000 {
		c.FIMWatchMaxDirs = 1024
	}
	// head_bytes: 0 は無効（従来動作）、上限は 1 MiB。負値は 0 へ。
	if c.FIMWatchHeadBytes < 0 {
		c.FIMWatchHeadBytes = 0
	}
	if c.FIMWatchHeadBytes > 1048576 {
		c.FIMWatchHeadBytes = 1048576
	}
	if len(c.FIMWatchIgnore) > 64 {
		return fmt.Errorf("fim_watch_ignore は64件以内で指定してください: %d", len(c.FIMWatchIgnore))
	}
	for i, pat := range c.FIMWatchIgnore {
		if err := sanitizePath(pat); err != nil {
			return fmt.Errorf("fim_watch_ignore[%d]: %w", i, err)
		}
	}

	if len(c.SUIDPaths) > 64 {
		return fmt.Errorf("suid_paths は64件以内で指定してください: %d", len(c.SUIDPaths))
	}
	for i, p := range c.SUIDPaths {
		if err := sanitizePath(p); err != nil {
			return fmt.Errorf("suid_paths[%d]: %w", i, err)
		}
	}
	if c.SUIDScanInterval < 60 {
		c.SUIDScanInterval = 3600
	}
	if c.SUIDScanInterval > 86400 {
		c.SUIDScanInterval = 86400
	}
	if len(c.SUIDFastPaths) > 64 {
		return fmt.Errorf("suid_fast_paths は64件以内で指定してください: %d", len(c.SUIDFastPaths))
	}
	for i, p := range c.SUIDFastPaths {
		if err := sanitizePath(p); err != nil {
			return fmt.Errorf("suid_fast_paths[%d]: %w", i, err)
		}
	}
	if c.SUIDFastScanInterval < 5 {
		c.SUIDFastScanInterval = 10
	}
	if c.SUIDFastScanInterval > 3600 {
		c.SUIDFastScanInterval = 3600
	}
	// listen_port の再通知クールダウン: 負値は 0（無効）へ。上限は 1 日。
	if c.ListenPortRenotifyCooldownSec < 0 {
		c.ListenPortRenotifyCooldownSec = 0
	}
	if c.ListenPortRenotifyCooldownSec > 86400 {
		c.ListenPortRenotifyCooldownSec = 86400
	}
	// inotify の監視対象。空なら既定（動きの速い置き場）へ戻す。
	if len(c.SUIDWatchPaths) == 0 {
		c.SUIDWatchPaths = []string{"/tmp", "/var/tmp", "/dev/shm", "/run"}
	}
	if len(c.SUIDWatchPaths) > 64 {
		return fmt.Errorf("suid_watch_paths は64件以内で指定してください: %d", len(c.SUIDWatchPaths))
	}
	for i, p := range c.SUIDWatchPaths {
		if err := sanitizePath(p); err != nil {
			return fmt.Errorf("suid_watch_paths[%d]: %w", i, err)
		}
	}
	if c.IntegrityCheckInterval < 30 {
		c.IntegrityCheckInterval = 300
	}
	if c.IntegrityCheckInterval > 86400 {
		c.IntegrityCheckInterval = 86400
	}
	if c.SUIDScanHour < -1 || c.SUIDScanHour > 23 {
		c.SUIDScanHour = 3
	}

	if len(c.CronPaths) > 64 {
		return fmt.Errorf("cron_paths は64件以内で指定してください: %d", len(c.CronPaths))
	}
	for i, p := range c.CronPaths {
		if err := sanitizePath(p); err != nil {
			return fmt.Errorf("cron_paths[%d]: %w", i, err)
		}
	}

	// 自動ブロック設定の検証。
	switch c.BlockMode {
	case "off", "dry-run", "enforce":
	default:
		c.BlockMode = "off"
	}
	switch c.FirewallBackend {
	case "auto", "iptables", "nftables", "firewalld":
	default:
		c.FirewallBackend = "auto"
	}
	if c.BlockDurationSec < 60 {
		c.BlockDurationSec = 600
	}
	if c.BlockDurationSec > 86400*7 {
		c.BlockDurationSec = 86400 * 7
	}
	if len(c.BlockWhitelist) > 1024 {
		return fmt.Errorf("block_whitelist は1024件以内で指定してください: %d", len(c.BlockWhitelist))
	}
	for i, w := range c.BlockWhitelist {
		if net.ParseIP(w) == nil {
			if _, _, err := net.ParseCIDR(w); err != nil {
				return fmt.Errorf("block_whitelist[%d]: IP または CIDR で指定してください: %s", i, w)
			}
		}
	}
	return nil
}

// sanitizePath rejects paths containing NUL/newlines or '..'.
func sanitizePath(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return fmt.Errorf("パスが空です")
	}
	if strings.ContainsAny(p, "\x00\n\r") {
		return fmt.Errorf("パスに不正な文字が含まれています")
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("パスに '..' は使用できません: %s", p)
	}
	return nil
}

// validateWatchPath validates a monitored log file path (read access).
// .log に加え、RHEL 系の /var/log/secure や messages も許可する。
func validateWatchPath(p string) error {
	if err := sanitizePath(p); err != nil {
		return err
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("絶対パスで指定してください: %s", p)
	}
	base := filepath.Base(p)
	if strings.HasSuffix(p, ".log") || base == "secure" || base == "messages" {
		return nil
	}
	return fmt.Errorf("対応していないログファイルです（.log / secure / messages のみ）: %s", p)
}

// validateOutputPath validates the plugin's own log path (write access).
func validateOutputPath(p string) error {
	if err := sanitizePath(p); err != nil {
		return err
	}
	if !strings.HasSuffix(p, ".log") {
		return fmt.Errorf("拡張子は .log のみ許可されます: %s", p)
	}
	return nil
}

func splitList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
