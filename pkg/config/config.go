package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentConfig はエージェントの設定を表す
type AgentConfig struct {
	DashboardURL string  `json:"dashboard_url"`
	Interval     float64 `json:"interval"`
	LogFile      string  `json:"log_file"`
	DiskPath     string  `json:"disk_path"`
}

// NotificationChannel は通知チャンネル1つ分の設定
type NotificationChannel struct {
	Type       string `json:"type"` // "discord" | "telegram" | "line"
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhook_url,omitempty"` // Discord
	BotToken   string `json:"bot_token,omitempty"`   // Telegram
	ChatID     string `json:"chat_id,omitempty"`     // Telegram
	Token      string `json:"token,omitempty"`       // LINE
}

// NotificationsConfig は通知全体の設定
type NotificationsConfig struct {
	Enabled  bool                  `json:"enabled"`
	Channels []NotificationChannel `json:"channels"`

	AgentTimeoutSec int `json:"agent_timeout_sec"` // Agent応答なしの閾値（秒）
	HoldSec         int `json:"hold_sec"`          // 異常が持続すべき秒数
	CooldownSec     int `json:"cooldown_sec"`      // 同一通知の再送禁止時間（秒）
	RecoveryHoldSec int `json:"recovery_hold_sec"` // 復旧判定のホールド秒数

	MemoryWarnPct       float64 `json:"memory_warn_pct"`
	MemoryCriticalPct   float64 `json:"memory_critical_pct"`
	DiskFreeWarnPct     float64 `json:"disk_free_warn_pct"`
	DiskFreeCriticalPct float64 `json:"disk_free_critical_pct"`
	CPUTempWarnC        float64 `json:"cpu_temp_warn_c"`
	CPUTempCriticalC    float64 `json:"cpu_temp_critical_c"`

	NotifyRecovery bool `json:"notify_recovery"`
}

// DashboardConfig はダッシュボードの設定を表す
type DashboardConfig struct {
	ListenAddr    string              `json:"listen_addr"`
	LogFile       string              `json:"log_file"`
	StaticDir     string              `json:"static_dir"`
	PluginsDir    string              `json:"plugins_dir"`            // ★ Phase 8-6 追加
	PluginsUpload *bool               `json:"plugins_upload_enabled"` // ★ Phase 8-6 追加（ポインタでnil=未設定を表現）
	Notifications NotificationsConfig `json:"notifications"`
}

// IsUploadEnabled はプラグインアップロードが有効かを返す。
// nil（未設定）ならデフォルトで false。明示的に true にしない限り無効や。
func (c *DashboardConfig) IsUploadEnabled() bool {
	if c.PluginsUpload == nil {
		return false
	}
	return *c.PluginsUpload
}

// ResolvePluginsDir は plugins ディレクトリの絶対パスを返す。
// 優先順位:
//  1. dashboard_config.json の plugins_dir（相対なら Abs で解決）
//  2. 実行ファイルと同じディレクトリの plugins/
//  3. カレントの ./plugins（os.Executable が取れへんときの保険）
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

// EnsurePluginsDir は plugins ディレクトリを必要に応じて作成する。
// 起動時に呼ぶことで、ユーザーが手動 mkdir せんで済む。
func (c *DashboardConfig) EnsurePluginsDir() (string, error) {
	dir := c.ResolvePluginsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("plugins ディレクトリの作成に失敗: %w", err)
	}
	return dir, nil
}

// LoadAgentConfig はエージェント設定ファイルを読み込む
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

	if err := validateAgentConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadDashboardConfig はダッシュボード設定ファイルを読み込む
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

	applyNotificationDefaults(&cfg.Notifications)

	if err := validateDashboardConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// applyNotificationDefaults は通知設定のゼロ値をデフォルトで埋める
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

// validateAgentConfig はエージェント設定を検証する
func validateAgentConfig(cfg *AgentConfig) error {
	if strings.TrimSpace(cfg.DashboardURL) == "" {
		return fmt.Errorf("DashboardURL が空です（必須）")
	}
	if !strings.HasPrefix(cfg.DashboardURL, "ws://") && !strings.HasPrefix(cfg.DashboardURL, "wss://") {
		fmt.Printf("[CONFIG] 警告: DashboardURL が ws:// または wss:// で始まっていません: %s\n", cfg.DashboardURL)
	}
	if cfg.Interval <= 0 {
		return fmt.Errorf("Interval は 0 より大きい値を指定してください (現在: %f)", cfg.Interval)
	}
	if cfg.Interval < 0.2 {
		fmt.Printf("[CONFIG] 警告: Interval が %.1f秒 と短すぎます。0.2秒以上を推奨します。\n", cfg.Interval)
	}
	if strings.TrimSpace(cfg.DiskPath) == "" {
		return fmt.Errorf("DiskPath が空です（必須）")
	}
	return nil
}

// validateDashboardConfig はダッシュボード設定を検証する
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
			switch ch.Type {
			case "discord":
				if ch.WebhookURL == "" {
					return fmt.Errorf("通知設定: discord には webhook_url が必須です")
				}
			case "telegram":
				if ch.BotToken == "" || ch.ChatID == "" {
					return fmt.Errorf("通知設定: telegram には bot_token と chat_id が必須です")
				}
			case "line":
				if ch.Token == "" {
					return fmt.Errorf("通知設定: line には token が必須です")
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

// EnsureDir は設定ファイルのディレクトリが存在することを確認する
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}
