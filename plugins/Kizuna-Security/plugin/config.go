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

	// ファイル完全性監視 (FIM)
	IntegrityFiles        []string // 改ざんを監視する重要ファイル
	IntegrityBaselinePath string   // ベースラインの保存先

	// 新規リッスンポート検知
	ListenPortCheck        bool
	ListenPortBaselinePath string

	// SUID/SGID 検知
	SUIDCheck        bool
	SUIDPaths        []string
	SUIDBaselinePath string
	SUIDScanInterval int // スキャン間隔（秒）
	// SUIDScanHour restricts the scan to a single hour of the day (0-23) so a
	// low-power host is not loaded during business hours. -1 disables the window.
	SUIDScanHour int

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

		ListenPortCheck:        true,
		ListenPortBaselinePath: "./logs/kizuna-security-ports.json",

		SUIDCheck:        true,
		SUIDPaths:        []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin", "/usr/local/bin", "/usr/local/sbin"},
		SUIDBaselinePath: "./logs/kizuna-security-suid.json",
		SUIDScanInterval: 3600,
		SUIDScanHour:     3,

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
		SudoIgnoreCommands:     []string{cronSudoHelper, "/usr/sbin/smartctl", "smartctl"},
		ChainKeyPath:           "/samba/share/Kizuna-Eye/keys/chain.key",
		IntegrityCheckInterval: 300,
		AlertHistoryPath:       "/samba/share/Kizuna-Eye/logs/alert_history.jsonl",
		AlertHistoryStatePath:  "./logs/kizuna-security-alertstate.json",
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

	if v, ok := raw["integrity_files"].(string); ok {
		c.IntegrityFiles = splitList(v)
	}
	if v, ok := raw["integrity_files"].([]interface{}); ok {
		c.IntegrityFiles = toStringList(v)
	}
	if v, ok := raw["integrity_baseline_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.IntegrityBaselinePath = v
	}

	if v, ok := toBool(raw["listen_port_check"]); ok {
		c.ListenPortCheck = v
	}
	if v, ok := raw["listen_port_baseline_path"].(string); ok && strings.TrimSpace(v) != "" {
		c.ListenPortBaselinePath = v
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
