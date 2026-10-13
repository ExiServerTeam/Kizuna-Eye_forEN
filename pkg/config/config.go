package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AgentConfig is the agent configuration.
type AgentConfig struct {
	DashboardURL string  `json:"dashboard_url"`
	Interval     float64 `json:"interval"`
	LogFile      string  `json:"log_file"`
	// LogLevel is "debug"|"info"|"warn"|"error". Empty defaults to "info".
	// DEBUG is verbose, so it is opt-in via this setting.
	LogLevel string `json:"log_level"`
	DiskPath string `json:"disk_path"`
	// Token authenticates the agent to the dashboard over WebSocket.
	// Empty disables agent authentication (backward compatible).
	Token string `json:"token"`

	// PluginsDir is the directory the agent may load plugin .so files from.
	// A plugin_path in modules.json that resolves outside this directory is
	// rejected, so a tampered modules.json cannot load an arbitrary .so.
	// Empty defaults to plugins/ next to the executable.
	PluginsDir string `json:"plugins_dir"`

	// AutoUpdate controls the GitHub Releases version check. When enabled and
	// a newer version is found, safe_update.sh is executed (which backs up the
	// binaries, rebuilds host + plugins, and rolls back on failure).
	AutoUpdate AutoUpdateConfig `json:"auto_update"`
}

// AutoUpdateConfig configures the automatic update check.
type AutoUpdateConfig struct {
	Enabled       bool   `json:"enabled"`
	RepositoryURL string `json:"repository_url"` // e.g. github.com/owner/Kizuna-Eye
	ArchiveName   string `json:"archive_name"`   // release asset name
	UpdateScript  string `json:"update_script"`  // default: safe_update.sh
	// IntervalHours is how often to check. Default 6.
	IntervalHours int `json:"interval_hours"`
}

// NotificationChannel is one notification channel config.
type NotificationChannel struct {
	Type       string `json:"type"` // "discord" | "telegram" | "line"
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhook_url,omitempty"` // Discord / Slack
	BotToken   string `json:"bot_token,omitempty"`   // Telegram
	ChatID     string `json:"chat_id,omitempty"`     // Telegram
	Token      string `json:"token,omitempty"`       // LINE

	// Email (SMTP)
	SMTPHost     string `json:"smtp_host,omitempty"`
	SMTPPort     string `json:"smtp_port,omitempty"`
	SMTPUsername string `json:"smtp_username,omitempty"`
	SMTPPassword string `json:"smtp_password,omitempty"`
	EmailFrom    string `json:"email_from,omitempty"`
	EmailTo      string `json:"email_to,omitempty"` // comma-separated
}

// NotificationsConfig is the notification configuration.
type NotificationsConfig struct {
	Enabled  bool                  `json:"enabled"`
	Channels []NotificationChannel `json:"channels"`

	AgentTimeoutSec int `json:"agent_timeout_sec"` // agent timeout (seconds)
	HoldSec         int `json:"hold_sec"`          // seconds an anomaly must persist
	CooldownSec     int `json:"cooldown_sec"`      // cooldown before re-notifying (seconds)
	RecoveryHoldSec int `json:"recovery_hold_sec"` // hold seconds for recovery

	MemoryWarnPct       float64 `json:"memory_warn_pct"`
	MemoryCriticalPct   float64 `json:"memory_critical_pct"`
	DiskFreeWarnPct     float64 `json:"disk_free_warn_pct"`
	DiskFreeCriticalPct float64 `json:"disk_free_critical_pct"`
	CPUTempWarnC        float64 `json:"cpu_temp_warn_c"`
	CPUTempCriticalC    float64 `json:"cpu_temp_critical_c"`

	NotifyRecovery bool `json:"notify_recovery"`
}

// AuthConfig controls the dashboard login and user management.
// When Enabled is false (or the block is absent), the dashboard behaves as
// before: no authentication. This keeps backward compatibility.
type AuthConfig struct {
	Enabled         bool   `json:"enabled"`
	SecureCookies   bool   `json:"secure_cookies"` // set true when served over HTTPS
	SessionTTLHours int    `json:"session_ttl_hours"`
	UsersFile       string `json:"users_file"` // default: users.json next to the dashboard config
	// AgentToken authenticates the agent's WebSocket connection. When empty,
	// the dashboard falls back to heuristic agent detection (legacy).
	AgentToken string `json:"agent_token"`

	// SessionFile persists browser sessions across restarts. Default:
	// sessions.json next to the dashboard config. Set to "none" to keep
	// sessions in memory only (logged out on every restart).
	SessionFile string `json:"session_file"`
	// SessionIPBind binds a session to the client IP that created it, so a
	// stolen cookie cannot be replayed from another address. Off by default
	// because a changing IP (DHCP, mobile) would log the user out. It is a
	// *bool so an absent key means "default (off)".
	SessionIPBind *bool `json:"session_ip_bind"`

	// GuestSessionTTLHours is the absolute lifetime of a guest session. A
	// guest is an anonymous viewer, so its session should expire far sooner
	// than a real user's. It is a *int so an absent key keeps the default
	// (12h). 0 or negative uses the manager's default TTL.
	GuestSessionTTLHours *int `json:"guest_session_ttl_hours"`

	// SessionIdleHours overrides the inactivity timeout. It is a *int so an
	// absent key keeps the default (2 hours):
	//   absent / nil    -> default (2h)
	//   0 (or negative) -> never time out on inactivity (the session lives
	//                      until SessionTTLHours expires)
	//   N > 0           -> N hours
	// Set this to 0 when "logged out when I come back" is unwanted.
	SessionIdleHours *int `json:"session_idle_hours"`

	// PublicViewer exposes the dashboard and the read-only monitoring
	// endpoints (CPU/memory/disk usage) to unauthenticated clients.
	// When true, a viewer can open the dashboard without logging in, but
	// only the dashboard page, /ws and /api/status are public: history,
	// alerts, logs, modules and user management still require a session.
	//
	// It is a *bool so an absent key means "use the default (enabled)".
	// A plain bool would deserialize a missing key as false, which would
	// silently disable the guest login on every existing config that
	// predates this option.
	PublicViewer *bool `json:"public_viewer"`
}

// SessionFilePath returns the session persistence file. configDir is the
// directory of dashboard_config.json. An empty result disables persistence.
// A relative path is resolved against configDir (NOT the process working
// directory), so the file location never depends on where the server was
// started from.
func (a AuthConfig) SessionFilePath(configDir string) string {
	v := strings.TrimSpace(a.SessionFile)
	if strings.EqualFold(v, "none") {
		return ""
	}
	if v == "" {
		return filepath.Join(configDir, "sessions.json")
	}
	if filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(configDir, v)
}

// IsSessionIPBind reports whether sessions are bound to their client IP.
// Defaults to false when unset.
func (a AuthConfig) IsSessionIPBind() bool {
	if a.SessionIPBind == nil {
		return false
	}
	return *a.SessionIPBind
}

// SessionIdleDuration returns the inactivity timeout for SessionManager.
//
//	 0 -> caller default (2h), when the key is absent
//	-1 -> idle timeout disabled
//	>0 -> that many hours
func (a AuthConfig) SessionIdleDuration() time.Duration {
	if a.SessionIdleHours == nil {
		return 0
	}
	if *a.SessionIdleHours <= 0 {
		return -1
	}
	return time.Duration(*a.SessionIdleHours) * time.Hour
}

// IsPublicViewer reports whether login-free viewer access is enabled.
// Defaults to true when unset.
func (a AuthConfig) IsPublicViewer() bool {
	if a.PublicViewer == nil {
		return true
	}
	return *a.PublicViewer
}

// GuestSessionTTL returns the absolute lifetime of a guest session. An absent
// key uses the default (12h); 0 or negative returns 0, which makes the auth
// handler fall back to the manager's own TTL.
func (a AuthConfig) GuestSessionTTL() time.Duration {
	if a.GuestSessionTTLHours == nil {
		return 12 * time.Hour
	}
	if *a.GuestSessionTTLHours <= 0 {
		return 0
	}
	return time.Duration(*a.GuestSessionTTLHours) * time.Hour
}

// DashboardConfig is the dashboard configuration.
type DashboardConfig struct {
	ListenAddr    string              `json:"listen_addr"`
	LogFile       string              `json:"log_file"`
	LogLevel      string              `json:"log_level"` // "debug"|"info"|"warn"|"error" (empty = debug)
	StaticDir     string              `json:"static_dir"`
	PluginsDir    string              `json:"plugins_dir"`
	PluginsUpload *bool               `json:"plugins_upload_enabled"` // nil means unset
	Notifications NotificationsConfig `json:"notifications"`
	Auth          AuthConfig          `json:"auth"`

	// AlertHistoryFile persists the alert history across restarts.
	// Empty uses the default (logs/alert_history.jsonl).
	AlertHistoryFile string `json:"alert_history_file"`
}

// IsUploadEnabled reports whether plugin uploads are enabled.
// Defaults to false when unset.
func (c *DashboardConfig) IsUploadEnabled() bool {
	if c.PluginsUpload == nil {
		return false
	}
	return *c.PluginsUpload
}

// ResolvePluginsDir returns the absolute plugins directory path.
// Priority:
//  1. plugins_dir from dashboard_config.json (relative resolved with Abs)
//  2. plugins/ next to the executable
//  3. ./plugins as a fallback
func (c *DashboardConfig) ResolvePluginsDir() string {
	if strings.TrimSpace(c.PluginsDir) != "" {
		abs, err := filepath.Abs(c.PluginsDir)
		if err == nil {
			return abs
		}
		return c.PluginsDir
	}
	exePath, err := os.Executable()
	if err != nil {
		return "./plugins"
	}
	return filepath.Join(filepath.Dir(exePath), "plugins")
}

// EnsurePluginsDir creates the plugins directory if needed.
// The directory holds executable .so files, so keep it owner-only.
func (c *DashboardConfig) EnsurePluginsDir() (string, error) {
	dir := c.ResolvePluginsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("plugins ディレクトリの作成に失敗: %w", err)
	}
	return dir, nil
}

// LoadAgentConfig reads the agent config file.
func LoadAgentConfig(path string) (*AgentConfig, error) {
	cfg := &AgentConfig{
		DashboardURL: "ws://localhost:8080/ws",
		Interval:     1.0,
		LogFile:      "",
		DiskPath:     "/",
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("[CONFIG] 設定ファイル %s が見つからないためデフォルト設定で起動します\n", path)
			return cfg, nil
		}
		return nil, fmt.Errorf("設定ファイル読み取りエラー: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("JSONパースエラー: %w", err)
	}

	fmt.Printf("[CONFIG] 設定ファイル %s を読み込みました\n", path)

	// The agent config holds the shared agent token; tighten the mode on
	// load so a file created world-readable is not left exposed.
	// Best-effort: ignore failure on filesystems that do not honour chmod.
	_ = os.Chmod(path, 0600)

	if err := validateAgentConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadDashboardConfig reads the dashboard config file.
func LoadDashboardConfig(path string) (*DashboardConfig, error) {
	cfg := &DashboardConfig{
		ListenAddr: ":8080",
		LogFile:    "",
		StaticDir:  "./web/static",
		Notifications: NotificationsConfig{
			Enabled:             false,
			AgentTimeoutSec:     30,
			HoldSec:             5,
			CooldownSec:         300,
			RecoveryHoldSec:     30,
			MemoryWarnPct:       80,
			MemoryCriticalPct:   90,
			DiskFreeWarnPct:     20,
			DiskFreeCriticalPct: 10,
			CPUTempWarnC:        70,
			CPUTempCriticalC:    85,
			NotifyRecovery:      true,
		},
		// 公開ビューア（ゲストログイン）は標準で有効。
		// 設定エディタの「認証」からオフにできる。
		Auth: AuthConfig{PublicViewer: boolPtr(true)},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("[CONFIG] 設定ファイル %s が見つからないためデフォルト設定で起動します\n", path)
			return cfg, nil
		}
		return nil, fmt.Errorf("設定ファイル読み取りエラー: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("JSONパースエラー: %w", err)
	}

	fmt.Printf("[CONFIG] 設定ファイル %s を読み込みました\n", path)

	// The dashboard config holds secrets (agent_token, webhook URLs, SMTP
	// password); tighten the mode on load. Best-effort.
	_ = os.Chmod(path, 0600)

	applyNotificationDefaults(&cfg.Notifications)

	if err := validateDashboardConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// applyNotificationDefaults fills zero values with defaults.
func applyNotificationDefaults(n *NotificationsConfig) {
	if n.AgentTimeoutSec <= 0 {
		n.AgentTimeoutSec = 30
	}
	if n.HoldSec <= 0 {
		n.HoldSec = 5
	}
	if n.CooldownSec <= 0 {
		n.CooldownSec = 300
	}
	if n.RecoveryHoldSec <= 0 {
		n.RecoveryHoldSec = 30
	}
	if n.MemoryWarnPct <= 0 {
		n.MemoryWarnPct = 80
	}
	if n.MemoryCriticalPct <= 0 {
		n.MemoryCriticalPct = 90
	}
	if n.DiskFreeWarnPct <= 0 {
		n.DiskFreeWarnPct = 20
	}
	if n.DiskFreeCriticalPct <= 0 {
		n.DiskFreeCriticalPct = 10
	}
	if n.CPUTempWarnC <= 0 {
		n.CPUTempWarnC = 70
	}
	if n.CPUTempCriticalC <= 0 {
		n.CPUTempCriticalC = 85
	}
}

// validateAgentConfig validates the agent config.
func validateAgentConfig(cfg *AgentConfig) error {
	if strings.TrimSpace(cfg.DashboardURL) == "" {
		return fmt.Errorf("DashboardURL が空です（必須）")
	}
	if err := ValidateAgentURL(cfg.DashboardURL); err != nil {
		return fmt.Errorf("dashboard_url が不正です: %w", err)
	}
	if cfg.Interval <= 0 {
		return fmt.Errorf("interval は 0 より大きい値を指定してください (現在: %f)", cfg.Interval)
	}
	if cfg.Interval < 0.2 {
		fmt.Printf("[CONFIG] 警告: Interval が %.1f秒 と短すぎます。0.2秒以上を推奨します。\n", cfg.Interval)
	}
	if strings.TrimSpace(cfg.DiskPath) == "" {
		return fmt.Errorf("DiskPath が空です（必須）")
	}
	return nil
}

// ResolvePluginsDir returns the absolute plugins directory for the agent.
// Priority: 1. plugins_dir from agent_config.json (relative resolved with Abs)
// 2. plugins/ next to the executable 3. ./plugins as a fallback.
func (c *AgentConfig) ResolvePluginsDir() string {
	if strings.TrimSpace(c.PluginsDir) != "" {
		abs, err := filepath.Abs(c.PluginsDir)
		if err == nil {
			return abs
		}
		return c.PluginsDir
	}
	exePath, err := os.Executable()
	if err != nil {
		return "./plugins"
	}
	return filepath.Join(filepath.Dir(exePath), "plugins")
}

// validateDashboardConfig validates the dashboard config.
func validateDashboardConfig(cfg *DashboardConfig) error {
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return fmt.Errorf("ListenAddr が空です（必須）")
	}
	if cfg.Notifications.Enabled {
		enabled := 0
		for _, ch := range cfg.Notifications.Channels {
			if !ch.Enabled {
				continue
			}
			enabled++
			// Accept any casing/whitespace (e.g. "Discord") so validation
			// matches the runtime channel dispatch in notify.FromConfig.
			switch strings.ToLower(strings.TrimSpace(ch.Type)) {
			case "discord":
				if ch.WebhookURL == "" {
					return fmt.Errorf("通知設定: discord には webhook_url が必須です")
				}
				if err := ValidateWebhookURL(ch.WebhookURL); err != nil {
					return fmt.Errorf("通知設定: discord の webhook_url が不正です: %w", err)
				}
			case "telegram":
				if ch.BotToken == "" || ch.ChatID == "" {
					return fmt.Errorf("通知設定: telegram には bot_token と chat_id が必須です")
				}
			case "line":
				if ch.Token == "" {
					return fmt.Errorf("通知設定: line には token が必須です")
				}
			case "slack":
				if ch.WebhookURL == "" {
					return fmt.Errorf("通知設定: slack には webhook_url が必須です")
				}
				if err := ValidateWebhookURL(ch.WebhookURL); err != nil {
					return fmt.Errorf("通知設定: slack の webhook_url が不正です: %w", err)
				}
			case "email":
				if ch.SMTPHost == "" || ch.EmailTo == "" {
					return fmt.Errorf("通知設定: email には smtp_host と email_to が必須です")
				}
			default:
				return fmt.Errorf("通知設定: 不明な type です: %s", ch.Type)
			}
		}
		if enabled == 0 {
			fmt.Printf("[CONFIG] 警告: notifications.enabled=true ですが有効なチャンネルがありません\n")
		}
	}
	return nil
}

// EnsureDir ensures the config file directory exists.
// Config files hold secrets, so the directory is created owner-only.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0700)
}

// boolPtr returns a pointer to v, for optional boolean config fields.
func boolPtr(v bool) *bool { return &v }
