package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"Kizuna-Eye/pkg/fsutil"
)

// AgentConfig は agent の設定だ。
// AgentConfig is the agent configuration.
type AgentConfig struct {
	DashboardURL string  `json:"dashboard_url"`
	Interval     float64 `json:"interval"`
	LogFile      string  `json:"log_file"`
	// LogLevel は "debug"|"info"|"warn"|"error" だ。空なら "info" を使う。
	// DEBUG は冗長なので、この設定で明示的に有効化する。
	// LogLevel is "debug"|"info"|"warn"|"error". Empty defaults to "info".
	// DEBUG is verbose, so it is opt-in via this setting.
	LogLevel string `json:"log_level"`
	DiskPath string `json:"disk_path"`
	// Token は agent を WebSocket 経由で dashboard に認証する。
	// 空なら agent 認証を無効化する（後方互換）。
	// Token authenticates the agent to the dashboard over WebSocket.
	// Empty disables agent authentication (backward compatible).
	Token string `json:"token"`

	// PluginsDir は agent がプラグイン .so を読み込めるディレクトリだ。
	// modules.json の plugin_path がこの外を指す場合は拒否する。
	// これにより、改ざんされた modules.json が任意の .so を読み込むのを防ぐ。
	// 空なら実行ファイル隣の plugins/ を使う。
	// PluginsDir is the directory the agent may load plugin .so files from.
	// A plugin_path in modules.json that resolves outside this directory is
	// rejected, so a tampered modules.json cannot load an arbitrary .so.
	// Empty defaults to plugins/ next to the executable.
	PluginsDir string `json:"plugins_dir"`

	// Plugins はプラグイン .so の A-3 認証ポリシーだ。
	// require_signature=true なら、agent は plugin.Open の前に
	// <plugin>.so.sig を Plugins.PublicKeyFile で検証する。
	// これにより、改ざん・未署名の .so は決して実行されない。
	// Plugins is the A-3 authentication policy for plugin .so files. With
	// require_signature=true the agent verifies <plugin>.so.sig against
	// Plugins.PublicKeyFile before calling plugin.Open, so a tampered or
	// unsigned .so is never executed.
	Plugins PluginSecurityConfig `json:"plugins"`

	// AutoUpdate は GitHub Releases のバージョンチェックを制御する。
	// 有効かつ新バージョン検知時は safe_update.sh を実行する
	// （バイナリを退避し、host + plugins を再ビルドし、失敗時はロールバックする）。
	// AutoUpdate controls the GitHub Releases version check. When enabled and
	// a newer version is found, safe_update.sh is executed (which backs up the
	// binaries, rebuilds host + plugins, and rolls back on failure).
	AutoUpdate AutoUpdateConfig `json:"auto_update"`

	// LogForward はログ行をリモート HTTP(S) エンドポイントへ転送する（opt-in）。
	// LogForward mirrors log lines to a remote HTTP(S) endpoint (opt-in).
	LogForward LogForwardConfig `json:"log_forward"`

	// ConfigVersion は設定スキーマのバージョンだ。新規設定にはインストーラが刻む。
	// 0（未設定）は従来動作を維持するため、アップグレードで既存環境が変わらない。
	// 詳細は CurrentConfigVersion を参照。
	// ConfigVersion is the config schema version. It is stamped by the
	// installer for new configs. 0 (absent) keeps the legacy behavior so an
	// upgrade never changes an existing installation. See CurrentConfigVersion.
	ConfigVersion int `json:"config_version,omitempty"`
}

// LogForwardConfig はローカルログ行をリモートコレクタへ転送する設定だ。
// 既定では無効で、ログを決してブロックしない。有界なメモリ内キューを使い、
// コレクタが遅い・到達不能な場合は行を捨てる。
// LogForwardConfig mirrors local log lines to a remote collector. It is off by
// default and never blocks logging: a bounded in-memory queue drops lines when
// the collector is slow or unreachable.
type LogForwardConfig struct {
	// URL は 1 POST につき 1 行（application/x-ndjson）を受け取る http(s) エンドポイントだ。
	// 空なら転送を無効化する。
	// URL is the http(s) endpoint that receives one log line per POST
	// (application/x-ndjson). Empty disables forwarding.
	URL string `json:"url"`
	// TimeoutSec は 1 回の転送リクエストの上限だ（既定: 5）。
	// TimeoutSec bounds one forward request (default: 5).
	TimeoutSec int `json:"timeout_sec"`
}

// Enabled は転送が設定済みかを返す。
// Enabled reports whether forwarding is configured.
func (c LogForwardConfig) Enabled() bool { return strings.TrimSpace(c.URL) != "" }

// Timeout はリクエストタイムアウトを返す。既定は 5 秒だ。
// Timeout returns the request timeout, defaulting to 5s.
func (c LogForwardConfig) Timeout() time.Duration {
	if c.TimeoutSec <= 0 {
		return 5 * time.Second
	}
	return time.Duration(c.TimeoutSec) * time.Second
}

// ValidateForwardURL はログ転送 URL を検証する。ホスト付きの http/https のみ許可する。
// webhook で使う SSRF プライベート IP ルールは適用しない。
// LAN 上のログコレクタ（例: http://192.168.0.10:9200）は正常で有用な配備であり、
// ここでプライベートアドレスを遮断すると機能が使えなくなるためだ。
// 値は運用者（admin）が設定し、リダイレクトはクライアントが拒否するので、
// 影響範囲は運用者自身の選択に留まる。
// ValidateForwardURL checks a log-forward URL. Only http/https with a host is
// accepted. It does not apply the SSRF private-IP rules used for webhooks: a
// log collector on the LAN (e.g. http://192.168.0.10:9200) is a normal, useful
// deployment, so blocking private addresses here would make the feature
// unusable. The value is operator-configured (admin), and redirects are
// refused by the client, so the blast radius stays the operator's own choice.
func ValidateForwardURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("log_forward.url を解析できません: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("log_forward.url は http または https のみ許可されます: %s", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("log_forward.url にホストがありません")
	}
	return nil
}

// AutoUpdateConfig は自動更新チェックを設定する。
// AutoUpdateConfig configures the automatic update check.
type AutoUpdateConfig struct {
	Enabled       bool   `json:"enabled"`
	RepositoryURL string `json:"repository_url"` // 例: github.com/owner/Kizuna-Eye
	ArchiveName   string `json:"archive_name"`   // リリースアセット名
	UpdateScript  string `json:"update_script"`  // 既定: safe_update.sh
	// IntervalHours はチェック間隔だ。既定 6。
	// IntervalHours is how often to check. Default 6.
	IntervalHours int `json:"interval_hours"`
}

// PluginSecurityConfig はプラグイン（.so）の認証方法と、導入前の検査方法を設定する
// （ハードニング項目 A-3）。agent と dashboard は同じキーを読むため、
// 1 つのポリシーが両方の経路を統治する。
//
// JSON（agent_config.json と dashboard_config.json の両方）:
//
//	"plugins": {
//	  "require_signature": true,
//	  "public_key_file": "/etc/kizuna-eye/plugin_signing.pub",
//	  "inspect_isolation": "bwrap"
//	}
//
// PluginSecurityConfig configures how plugin (.so) files are authenticated and
// how they are inspected before installation (hardening item A-3). The agent
// and the dashboard read the same keys so one policy governs both paths.
//
// JSON (both agent_config.json and dashboard_config.json):
//
//	"plugins": {
//	  "require_signature": true,
//	  "public_key_file": "/etc/kizuna-eye/plugin_signing.pub",
//	  "inspect_isolation": "bwrap"
//	}
type PluginSecurityConfig struct {
	// RequireSignature は分離署名（<file>.so.sig）が無い・検証できない .so を拒否する。
	// false は従来動作（未署名 .so を読み込む）を維持し、アップグレードを妨げない。
	// RequireSignature rejects any .so whose detached signature
	// (<file>.so.sig) is missing or does not verify. false keeps the legacy
	// behaviour (an unsigned .so is loaded) so upgrades are not disruptive.
	RequireSignature bool `json:"require_signature"`

	// PublicKeyFile は署名検証に使う Ed25519 公開鍵だ。
	// RequireSignature が true の場合は必須。相対パスは設定ファイルのディレクトリを基準に解決する。
	// PublicKeyFile is the Ed25519 public key used to verify signatures.
	// Required when RequireSignature is true. A relative path is resolved
	// against the config file's directory.
	PublicKeyFile string `json:"public_key_file"`

	// InspectIsolation はアップロードされた .so に対する plugin-inspect の実行方法を選ぶ。
	// "bwrap"（既定）はネットワーク無し・書き込み可能ホーム無しの bubblewrap 内で実行し、
	// "off" は従来どおり直接実行する。bwrap 指定で利用不可の場合はアップロードを拒否する（fail-closed）。
	// InspectIsolation selects how plugin-inspect is executed for an
	// uploaded .so: "bwrap" (default) runs it inside bubblewrap with no
	// network and no writable home, "off" runs it directly as before. When
	// bwrap is selected but unavailable, uploads are rejected (fail-closed).
	InspectIsolation string `json:"inspect_isolation"`

	// BwrapPath は bubblewrap バイナリの場所を上書きする。
	// 空なら /usr/bin/bwrap を使う。
	// BwrapPath overrides the bubblewrap binary location.
	// Empty uses /usr/bin/bwrap.
	BwrapPath string `json:"bwrap_path"`
}

// WantSignature は検証不能な .so を拒否すべきかを返す。
// WantSignature reports whether an unverifiable .so must be rejected.
func (c PluginSecurityConfig) WantSignature() bool { return c.RequireSignature }

// WantInspectIsolation は plugin-inspect を bwrap 下で実行すべきかを返す。
// 明示的な "off" のみ無効化するため、キー未設定時は安全な既定が適用される。
// WantInspectIsolation reports whether plugin-inspect must run under bwrap.
// Only an explicit "off" disables it, so the safe default applies when the
// key is absent.
func (c PluginSecurityConfig) WantInspectIsolation() bool {
	return !strings.EqualFold(strings.TrimSpace(c.InspectIsolation), "off")
}

// BwrapBinary は使用する bubblewrap のパスを返す。
// BwrapBinary returns the bubblewrap path to use.
func (c PluginSecurityConfig) BwrapBinary() string {
	if p := strings.TrimSpace(c.BwrapPath); p != "" {
		return p
	}
	return "/usr/bin/bwrap"
}

// Validate はポリシーを強制できない場合に fail-closed のエラーを返す。
// Validate returns a fail-closed error when the policy cannot be enforced.
func (c PluginSecurityConfig) Validate() error {
	if c.RequireSignature && strings.TrimSpace(c.PublicKeyFile) == "" {
		return fmt.Errorf("plugins: require_signature=true には public_key_file が必須です（Ed25519 公開鍵のパス）")
	}
	return nil
}

// NotificationChannel は通知チャンネル 1 件分の設定だ。
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
	EmailTo      string `json:"email_to,omitempty"` // カンマ区切り
}

// NotificationsConfig は通知設定だ。
// NotificationsConfig is the notification configuration.
type NotificationsConfig struct {
	Enabled  bool                  `json:"enabled"`
	Channels []NotificationChannel `json:"channels"`

	AgentTimeoutSec int `json:"agent_timeout_sec"` // agent タイムアウト（秒）
	HoldSec         int `json:"hold_sec"`          // 異常が持続すべき秒数
	CooldownSec     int `json:"cooldown_sec"`      // 再通知までのクールダウン（秒）
	RecoveryHoldSec int `json:"recovery_hold_sec"` // 復旧の保持秒数

	MemoryWarnPct       float64 `json:"memory_warn_pct"`
	MemoryCriticalPct   float64 `json:"memory_critical_pct"`
	DiskFreeWarnPct     float64 `json:"disk_free_warn_pct"`
	DiskFreeCriticalPct float64 `json:"disk_free_critical_pct"`
	CPUTempWarnC        float64 `json:"cpu_temp_warn_c"`
	CPUTempCriticalC    float64 `json:"cpu_temp_critical_c"`

	NotifyRecovery bool `json:"notify_recovery"`

	// --- 通知の信頼性 (タスク1) ---
	// Discord は短時間に大量の Webhook を送ると HTTP 429 (rate limit)
	// を返す。実機の攻撃テストで 393 件中 273 件が未達になったため、
	// リトライと集約を追加する。
	//
	// DiscordMaxRetries is the maximum number of 429/5xx retries per send.
	// It is a *int so an absent key keeps the default (5) instead of Go's
	// zero value: with a plain int, a config that never set the key silently
	// disabled retry (0) and every 429 was dropped unretried. An explicit 0
	// still disables retry (legacy behaviour).
	// omitempty: a nil pointer is left out when the config is written back
	// (AlertConfigHandler.persistConfig marshals the whole struct), so the
	// file keeps documenting the default instead of gaining a "null" value.
	DiscordMaxRetries *int `json:"discord_max_retries,omitempty"`
	// DiscordBackoffMaxSec は指数バックオフの待機上限だ。既定: 60。
	// DiscordBackoffMaxSec caps the exponential backoff wait. Default: 60.
	DiscordBackoffMaxSec int `json:"discord_backoff_max_sec"`

	// BatchEnabled は近接して到着したアラートを 1 メッセージに集約し、
	// バーストによるレート制限を防ぐ。履歴はアラート単位で記録する（通知のみ集約）。
	// *bool なのでキー未設定は既定（true）を意味し、Go の false にはならない。
	// BatchEnabled groups alerts that arrive close together into one
	// message, so a burst does not trigger rate limiting. History is still
	// recorded per-alert (only the notification is batched). *bool so an
	// absent key means the default (true) rather than Go's false.
	BatchEnabled *bool `json:"batch_enabled"`
	// BatchWindowSec は集約ウィンドウだ。既定: 5。
	// BatchWindowSec is the aggregation window. Default: 5.
	BatchWindowSec int `json:"batch_window_sec"`
	// BatchExcludeCritical は critical を即時送信し、集約しない。
	// これにより単一の critical が遅延しない。既定: true。
	// BatchExcludeCritical sends critical alerts immediately instead of
	// batching them, so a single critical is never delayed. Default: true.
	BatchExcludeCritical *bool `json:"batch_exclude_critical"`

	// --- 週次セキュリティレポート (タスク10) ---
	// WeeklyReportEnabled は週次セキュリティブリーフィングを有効化する。
	// opt-in（nil/false = off）だ。レポートは情報提供目的であり、
	// 運用者が要求しない限りメール送信を始めてはならない。
	// *bool なのでキー未設定は既定（off）を意味し、Go のゼロ値にはならない。
	// WeeklyReportEnabled turns on the weekly security briefing. Opt-in
	// (nil/false = off): the report is informational and must not start
	// sending mail without the operator asking for it. *bool so an absent
	// key means the default (off) instead of Go's zero value.
	WeeklyReportEnabled *bool `json:"weekly_report_enabled,omitempty"`
	// WeeklyReportDay はレポート送信の曜日だ（0=日曜..6=土曜）。既定 1（月曜）。
	// WeeklyReportDay is the weekday the report is sent (0=Sunday..6=Saturday).
	// Default 1 (Monday).
	WeeklyReportDay int `json:"weekly_report_day"`
	// WeeklyReportHour はレポート送信の時刻だ（0-23）。既定 9。
	// WeeklyReportHour is the hour (0-23) the report is sent. Default 9.
	WeeklyReportHour int `json:"weekly_report_hour"`
	// WeeklyReportTo はレポート送信先の通知チャンネル一覧だ（例: ["email"]）。
	// 空なら ["email"] を使う。レポートはチャットよりメール向きの文書だからだ。
	// WeeklyReportTo lists the notification channels the report is sent to
	// (e.g. ["email"]). Empty means ["email"], since a report is a document
	// better suited to mail than to a chat channel.
	WeeklyReportTo []string `json:"weekly_report_to,omitempty"`
}

// IsWeeklyReportEnabled は週次セキュリティレポートが有効かを返す。
// IsWeeklyReportEnabled reports whether the weekly security report is on.
func (n *NotificationsConfig) IsWeeklyReportEnabled() bool {
	return n.WeeklyReportEnabled != nil && *n.WeeklyReportEnabled
}

// WeeklyReportChannels はレポートの送信先チャンネルを返す。未設定なら既定 ["email"]。
// WeeklyReportChannels returns the channels the report is sent to. Default
// ["email"] when unset.
func (n *NotificationsConfig) WeeklyReportChannels() []string {
	if len(n.WeeklyReportTo) == 0 {
		return []string{"email"}
	}
	return n.WeeklyReportTo
}

// WeeklyReportSchedule は曜日・時刻を正規化し、既定（月曜 09:00）を適用する。
// WeeklyReportSchedule normalizes the day/hour, applying the defaults
// (Monday 09:00).
func (n *NotificationsConfig) WeeklyReportSchedule() (day, hour int) {
	day, hour = n.WeeklyReportDay, n.WeeklyReportHour
	if day < 0 || day > 6 {
		day = 1
	}
	if hour < 0 || hour > 23 {
		hour = 9
	}
	return day, hour
}

// AuthConfig は dashboard のログインとユーザー管理を制御する。
// Enabled が false（またはブロック未設定）の場合、dashboard は従来どおり
// 認証なしで動作する。これにより後方互換を保つ。
// AuthConfig controls the dashboard login and user management.
// When Enabled is false (or the block is absent), the dashboard behaves as
// before: no authentication. This keeps backward compatibility.
type AuthConfig struct {
	Enabled         bool   `json:"enabled"`
	SecureCookies   bool   `json:"secure_cookies"` // HTTPS 配信時に true にする
	SessionTTLHours int    `json:"session_ttl_hours"`
	UsersFile       string `json:"users_file"` // 既定: dashboard 設定隣の users.json
	// AgentToken は agent の WebSocket 接続を認証する。空なら dashboard は
	// 従来のヒューリスティック判定にフォールバックする（legacy）。
	// AgentToken authenticates the agent's WebSocket connection. When empty,
	// the dashboard falls back to heuristic agent detection (legacy).
	AgentToken string `json:"agent_token"`

	// SessionFile は再起動をまたいでブラウザセッションを永続化する。既定:
	// dashboard 設定隣の sessions.json。"none" でメモリ内のみに保つ（再起動ごとにログアウト）。
	// SessionFile persists browser sessions across restarts. Default:
	// sessions.json next to the dashboard config. Set to "none" to keep
	// sessions in memory only (logged out on every restart).
	SessionFile string `json:"session_file"`
	// SessionIPBind はセッションを生成元クライアント IP に束縛し、盗まれた cookie の
	// 別アドレスからの再生を防ぐ。IP が変わる環境（DHCP、モバイル）ではログアウトを招くため
	// 既定 off。*bool なのでキー未設定は「既定（off）」を意味する。
	// SessionIPBind binds a session to the client IP that created it, so a
	// stolen cookie cannot be replayed from another address. Off by default
	// because a changing IP (DHCP, mobile) would log the user out. It is a
	// *bool so an absent key means "default (off)".
	SessionIPBind *bool `json:"session_ip_bind"`

	// GuestSessionTTLHours はゲストセッションの絶対寿命だ。
	// ゲストは匿名ビューアなので、実ユーザーよりはるかに早く失効させるべきだ。
	// *int なのでキー未設定は既定（12h）を保つ。0 以下は manager の既定 TTL を使う。
	// GuestSessionTTLHours is the absolute lifetime of a guest session. A
	// guest is an anonymous viewer, so its session should expire far sooner
	// than a real user's. It is a *int so an absent key keeps the default
	// (12h). 0 or negative uses the manager's default TTL.
	GuestSessionTTLHours *int `json:"guest_session_ttl_hours"`

	// SessionIdleHours は無操作タイムアウトを上書きする。*int なのでキー未設定は
	// 既定（2 時間）を保つ:
	//   未設定 / nil    -> 既定（2h）
	//   0（または負）   -> 無操作でタイムアウトしない（SessionTTLHours まで生存）
	//   N > 0           -> N 時間
	// 「戻ってきたらログアウトされていた」を避けたい場合は 0 に設定する。
	// SessionIdleHours overrides the inactivity timeout. It is a *int so an
	// absent key keeps the default (2 hours):
	//   absent / nil    -> default (2h)
	//   0 (or negative) -> never time out on inactivity (the session lives
	//                      until SessionTTLHours expires)
	//   N > 0           -> N hours
	// Set this to 0 when "logged out when I come back" is unwanted.
	SessionIdleHours *int `json:"session_idle_hours"`

	// PublicViewer は dashboard と読み取り専用の監視エンドポイント
	// （CPU/メモリ/ディスク使用率）を未認証クライアントへ公開する。
	// true のとき、viewer はログイン無しで dashboard を開けるが、公開されるのは
	// dashboard ページ・/ws・/api/status のみだ。履歴・アラート・ログ・モジュール・
	// ユーザー管理は依然としてセッションを要する。
	//
	// *bool なのでキー未設定は「既定（有効）」を意味する。
	// 単なる bool だと未設定キーが false にデシリアライズされ、
	// このオプション以前の既存設定すべてでゲストログインを黙って無効化してしまう。
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

// SessionFilePath はセッション永続化ファイルを返す。configDir は
// dashboard_config.json のディレクトリだ。空の結果は永続化を無効化する。
// 相対パスは configDir を基準に解決する（プロセスの作業ディレクトリではない）。
// これにより、ファイル位置がサーバーの起動場所に依存しない。
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

// IsSessionIPBind はセッションがクライアント IP に束縛されるかを返す。未設定時は false。
// IsSessionIPBind reports whether sessions are bound to their client IP.
// Defaults to false when unset.
func (a AuthConfig) IsSessionIPBind() bool {
	if a.SessionIPBind == nil {
		return false
	}
	return *a.SessionIPBind
}

// SessionIdleDuration は SessionManager 用の無操作タイムアウトを返す。
//
//	 0 -> キー未設定時、呼び出し側の既定（2h）
//	-1 -> 無操作タイムアウト無効
//	>0 -> その時間数
//
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

// IsPublicViewer はログイン不要の viewer アクセスが有効かを返す。未設定時は true。
// IsPublicViewer reports whether login-free viewer access is enabled.
// Defaults to true when unset.
func (a AuthConfig) IsPublicViewer() bool {
	if a.PublicViewer == nil {
		return true
	}
	return *a.PublicViewer
}

// GuestSessionTTL はゲストセッションの絶対寿命を返す。キー未設定は既定（12h）、
// 0 以下は 0 を返し、auth ハンドラが manager 自身の TTL にフォールバックする。
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

// DashboardConfig は dashboard の設定だ。
// DashboardConfig is the dashboard configuration.
type DashboardConfig struct {
	ListenAddr string `json:"listen_addr"`
	LogFile    string `json:"log_file"`
	LogLevel   string `json:"log_level"` // "debug"|"info"|"warn"|"error"（空 = debug）
	StaticDir  string `json:"static_dir"`
	// Language は既定の UI 言語だ（"ja" | "en"）。ブラウザに保存設定が無い場合の
	// フォールバックになる。空または不明なら "en" を使う。
	// Language is the default UI language ("ja" | "en"). It is the fallback
	// when the browser has no saved preference. Empty or unknown means "en".
	Language      string `json:"language"`
	PluginsDir    string `json:"plugins_dir"`
	PluginsUpload *bool  `json:"plugins_upload_enabled"` // nil は未設定を意味する

	// Plugins は A-3 ポリシーだ。アップロードされた .so の署名検証と、
	// plugin-inspect のサンドボックス実行を担う。
	// Plugins is the A-3 policy: signature verification of uploaded .so
	// files and sandboxed execution of plugin-inspect.
	Plugins       PluginSecurityConfig `json:"plugins"`
	Notifications NotificationsConfig  `json:"notifications"`
	Auth          AuthConfig           `json:"auth"`

	// AlertHistoryFile はアラート履歴を再起動をまたいで永続化する。
	// 空なら既定（logs/alert_history.jsonl）を使う。
	// AlertHistoryFile persists the alert history across restarts.
	// Empty uses the default (logs/alert_history.jsonl).
	AlertHistoryFile string `json:"alert_history_file"`

	// AlertHistoryKeyPath は永続化するアラート履歴の各行を署名する HMAC 鍵だ。
	// この用途専用の鍵を使うこと（セキュリティログのチェーン鍵を流用しない）。
	// 用途ごとに鍵を分けると漏洩時の影響範囲を限定でき、独立にローテーションできる。
	// 相対パスは設定ディレクトリを基準に解決する。空なら署名を無効化する（後方互換）。
	// AlertHistoryKeyPath is the HMAC key used to sign each persisted alert
	// history line. It must be a key dedicated to this purpose (do not reuse
	// the security-log chain key): one key per use limits the blast radius of
	// a leak and lets each key be rotated independently. A relative path is
	// resolved against the config directory. Empty disables signing
	// (backward compatible).
	AlertHistoryKeyPath string `json:"alert_history_key_path"`

	// LogForward はログ行をリモート HTTP(S) エンドポイントへ転送する（opt-in）。
	// LogForward mirrors log lines to a remote HTTP(S) endpoint (opt-in).
	LogForward LogForwardConfig `json:"log_forward"`

	// ConfigVersion は設定スキーマのバージョンだ。新規設定はインストーラが刻み、
	// ローダーはこれを見て、キー未設定時にセキュア既定を適用するかを決める。
	// 0（未設定）は従来既定を維持するため、アップグレードで既存環境が変わらない。
	// 詳細は CurrentConfigVersion を参照。
	// ConfigVersion is the config schema version. New configs are stamped by
	// the installer; the loader uses it to decide whether secure defaults
	// apply to keys that are absent. 0 (absent) keeps the legacy defaults so
	// an upgrade never changes an existing installation. See
	// CurrentConfigVersion.
	ConfigVersion int `json:"config_version,omitempty"`
}

// IsUploadEnabled はプラグインアップロードが有効かを返す。未設定時は false。
// IsUploadEnabled reports whether plugin uploads are enabled.
// Defaults to false when unset.
func (c *DashboardConfig) IsUploadEnabled() bool {
	if c.PluginsUpload == nil {
		return false
	}
	return *c.PluginsUpload
}

// ResolvePluginsDir はプラグインディレクトリの絶対パスを返す。
// 優先順:
//  1. dashboard_config.json の plugins_dir（相対は Abs で解決）
//  2. 実行ファイル隣の plugins/
//  3. フォールバックとして ./plugins
//
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

// EnsurePluginsDir はプラグインディレクトリを必要に応じて作成する。
// このディレクトリは実行可能な .so を保持する。所有者のみ書き込み可能に保ち、
// dashboard ユーザーだけがプラグインを配置できるようにするが、agent は
// 通過と読み取りができる必要がある。A-4 で agent は別ユーザー（kizuna-eye）で
// 動くため、以前の所有者のみ 0700 ではなく 0755 を使う。
// EnsurePluginsDir creates the plugins directory if needed.
// The directory holds executable .so files: it must stay owner-writable so
// only the dashboard user can place plugins, but the agent has to traverse and
// read it. With A-4 the agent runs as a separate user (kizuna-eye), so 0755
// is used instead of the previous owner-only 0700.
func (c *DashboardConfig) EnsurePluginsDir() (string, error) {
	dir := c.ResolvePluginsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("plugins ディレクトリの作成に失敗: %w", err)
	}
	return dir, nil
}

// absFromConfigDir は相対パスを、それを読んだ設定ファイルのディレクトリを基準に解決する（Medium-10）。
// 絶対値はそのまま返し、空値は空のまま返す。これにより、文書化された既定
// （設定隣の users.json / sessions.json、./web/static、バイナリ隣の plugins/）が
// 引き続き機能する。
// absFromConfigDir resolves a relative path against the directory of the
// config file it was read from (Medium-10). Absolute values are returned as-is
// and empty values stay empty, so the documented defaults (users.json /
// sessions.json next to the config, ./web/static, plugins/ next to the binary)
// keep working.
func absFromConfigDir(configDir, v string) string {
	v = strings.TrimSpace(v)
	if v == "" || configDir == "" || filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(configDir, v)
}

// resolvePaths は agent の相対パスを設定ディレクトリに固定する。
//
// これ以前は、相対 "logs/agent.log" はプロセスの作業ディレクトリを基準に解決されていた。
// start.sh はリポジトリへ cd するので動いたが、systemd・cron・別の場所の -config では
// 黙って別の場所へログを書き（そしてプラグインを探し）ていた。
// resolvePaths anchors the agent's relative paths at the config directory.
//
// Before this, a relative "logs/agent.log" was resolved against the process
// working directory: start.sh cd's into the repository so it worked, but
// systemd, a cron job, or a -config path elsewhere silently wrote the log (and
// looked for plugins) somewhere else.
func (c *AgentConfig) resolvePaths(configDir string) {
	c.LogFile = absFromConfigDir(configDir, c.LogFile)
	c.PluginsDir = absFromConfigDir(configDir, c.PluginsDir)
	c.Plugins.PublicKeyFile = absFromConfigDir(configDir, c.Plugins.PublicKeyFile)
}

// resolvePaths は dashboard の相対データパスを設定ディレクトリに固定する。
// アラート履歴とログはデータなので、プロセスの起動場所に依存してはならない
// （別の cwd からの再起動で履歴が空に見えた）。static_dir は意図的にここで
// 解決しない。静的 UI は設定ファイル隣ではなくリポジトリ（./web/static）に同梱される。
// resolvePaths anchors the dashboard's relative data paths at the config
// directory. Alert history and logs are data, so they must not depend on where
// the process was started from (a restart from another cwd made the history
// look empty). static_dir is intentionally NOT resolved here: the static UI is
// shipped in the repository (./web/static), not next to the config file.
func (c *DashboardConfig) resolvePaths(configDir string) {
	c.LogFile = absFromConfigDir(configDir, c.LogFile)
	c.AlertHistoryFile = absFromConfigDir(configDir, c.AlertHistoryFile)
	c.AlertHistoryKeyPath = absFromConfigDir(configDir, c.AlertHistoryKeyPath)
	c.PluginsDir = absFromConfigDir(configDir, c.PluginsDir)
	c.Plugins.PublicKeyFile = absFromConfigDir(configDir, c.Plugins.PublicKeyFile)
}

// LoadAgentConfig は agent 設定ファイルを読む。
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

	// agent 設定は共有 agent トークンを保持する。読み込み時にモードを締め、
	// world-readable で作られたファイルを露出したままにしない。
	//
	// A-4: agent は kizuna-eye として動き、dashboard（およびこのファイルを所有する
	// 運用者）はログインユーザーとして動く。そのため移行は共有 kizuna-eye グループに
	// 読み取り（0640）を与える。ここで 0600 に締めると、dashboard 再起動直後に
	// agent が設定を読めなくなるため、意図的なグループ読み取りビットは保持する。
	// ベストエフォート: chmod を尊重しないファイルシステムでの失敗は無視する。
	// The agent config holds the shared agent token; tighten the mode on
	// load so a file created world-readable is not left exposed.
	//
	// A-4: the agent runs as kizuna-eye while the dashboard (and the
	// operator who owns this file) runs as the login user, so the migration
	// grants the shared kizuna-eye group read (0640). Tightening to 0600
	// here would make the config unreadable for the agent as soon as the
	// dashboard restarted, so a deliberate group-read bit is preserved.
	// Best-effort: ignore failure on filesystems that do not honour chmod.
	fsutil.TightenSharedConfigMode(path)

	// 相対パス（log_file, plugins_dir）をプロセスの作業ディレクトリではなく
	// 設定ファイルのディレクトリに固定する（Medium-10）。
	// Anchor relative paths (log_file, plugins_dir) at the config file's
	// directory instead of the process working directory (Medium-10).
	cfg.resolvePaths(filepath.Dir(path))

	if err := validateAgentConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// CurrentConfigVersion はセキュア既定にオプトインする設定スキーマのバージョンだ。
// config_version が無い（または古い）設定は従来既定を維持するため、
// アップグレードで既存環境が変わらない。真新しい設定（ファイル無し）と、
// このバージョンを宣言した設定は、未設定キーにセキュア既定を得る。
// CurrentConfigVersion is the config schema version that opts into the secure
// defaults. A config without config_version (or with an older value) keeps the
// legacy defaults so upgrading never changes an existing installation; a
// brand-new config (no file) and configs that declare this version get the
// secure defaults for keys they leave unset.
const CurrentConfigVersion = 2

// SecureDefaultsEnabled は生の設定がセキュア既定にオプトインしているか、
// すなわち config_version >= CurrentConfigVersion を宣言しているかを返す。
// config_version が無い（legacy）または古い値の設定は従来既定を維持する。
// LoadDashboardConfig の外で DashboardConfig をアンマーシャルする呼び出し側
// （ローダーの副作用を起こしてはならない insight ポスチャチェックなど）が、
// 「未設定キー」を同じように解決できるようエクスポートしている。
// SecureDefaultsEnabled reports whether a raw config opts into the secure
// defaults, i.e. it declares config_version >= CurrentConfigVersion. A config
// with no config_version (legacy) or an older value keeps the historical
// defaults. It is exported so callers that unmarshal a DashboardConfig outside
// LoadDashboardConfig (e.g. the insight posture check, which must not perform
// the loader's side effects) can resolve "absent key" the same way.
func SecureDefaultsEnabled(raw []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	v, ok := m["config_version"]
	if !ok {
		return false
	}
	var ver int
	return json.Unmarshal(v, &ver) == nil && ver >= CurrentConfigVersion
}

// EffectivePublicViewer は config_version が configVersion である設定の、
// 解決済み public_viewer 値を返す。キー未設定時、セキュアな config_version には
// セキュア既定（false）が、それ以外には従来既定（true）が適用される。
// 既定を事前充填しない素の json.Unmarshal で生成した struct に対しては、
// IsPublicViewer の代わりにこれを使う。
// EffectivePublicViewer returns the resolved public_viewer value for a config
// whose config_version is configVersion. When the key is absent, the secure
// default applies to a secure config_version (false) and the legacy default
// applies otherwise (true). Use this instead of IsPublicViewer when the struct
// was produced by a bare json.Unmarshal that did not pre-fill defaults.
func (a AuthConfig) EffectivePublicViewer(configVersion int) bool {
	if a.PublicViewer != nil {
		return *a.PublicViewer
	}
	return configVersion < CurrentConfigVersion
}

// defaultDashboardConfig は組み込みの既定を返す。secure が true なら、
// 設定が未設定のままにするキーにハードニング済み既定を使う
// （public_viewer=false: 読み取り専用 viewer エンドポイントはログインの背後に留まる）。
// secure は legacy 設定に対して false で、未設定キーが従来動作を保つ。
// listen_addr は両ケースで意図的に ":8080" のままにする。これは LAN 監視ツールで、
// 全インターフェースへのバインドが通常の文書化された配備だからだ。
// loopback のみを望む運用者は明示的に設定する。
// defaultDashboardConfig returns the built-in defaults. When secure is true,
// the hardened defaults are used for keys a config leaves unset
// (public_viewer=false: the read-only viewer endpoints stay behind login).
// secure is false for legacy configs so their absent keys keep the historical
// behavior. listen_addr is intentionally left as ":8080" in both cases: this
// is a LAN monitoring tool and binding to all interfaces is its normal,
// documented deployment; operators who want loopback-only set it explicitly.
func defaultDashboardConfig(secure bool) *DashboardConfig {
	publicViewer := true
	if secure {
		publicViewer = false
	}
	return &DashboardConfig{
		ListenAddr: ":8080",
		LogFile:    "",
		StaticDir:  "./web/static",
		Language:   "en",
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
		Auth: AuthConfig{PublicViewer: boolPtr(publicViewer)},
	}
}

// LoadDashboardConfig は dashboard 設定ファイルを読む。
// LoadDashboardConfig reads the dashboard config file.
func LoadDashboardConfig(path string) (*DashboardConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 真新しいインストール（設定無し）はセキュア既定を得て、現在バージョンが
			// 刻まれる。これにより、後でキーを省いた手編集でもセキュア値に解決される。
			// A brand-new install (no config) gets the secure defaults, and is
			// stamped with the current version so a later hand-edit that omits
			// a key still resolves to the secure value.
			cfg := defaultDashboardConfig(true)
			cfg.ConfigVersion = CurrentConfigVersion
			fmt.Printf("[CONFIG] 設定ファイル %s が見つからないためデフォルト設定で起動します\n", path)
			return cfg, nil
		}
		return nil, fmt.Errorf("設定ファイル読み取りエラー: %w", err)
	}

	secure := SecureDefaultsEnabled(data)

	cfg := defaultDashboardConfig(secure)
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("JSONパースエラー: %w", err)
	}

	fmt.Printf("[CONFIG] 設定ファイル %s を読み込みました\n", path)

	// dashboard 設定は秘密（agent_token、webhook URL、SMTP パスワード）を保持する。
	// 読み込み時にモードを締める。ベストエフォート。明示的に与えられたグループ読み取りは
	// 保持する（LoadAgentConfig / fsutil.TightenSharedConfigMode 参照）。
	// The dashboard config holds secrets (agent_token, webhook URLs, SMTP
	// password); tighten the mode on load. Best-effort. Group read is kept
	// if explicitly granted (see LoadAgentConfig / fsutil.TightenSharedConfigMode).
	fsutil.TightenSharedConfigMode(path)

	applyNotificationDefaults(&cfg.Notifications)

	// 相対データパス（log_file, alert_history_file, plugins_dir）をプロセスの
	// 作業ディレクトリではなく設定ファイルのディレクトリに固定する（Medium-10）。
	// static_dir は意図的に触らない。UI は dashboard_config.json 隣ではなく
	// リポジトリに ./web/static として同梱される。
	// Anchor relative data paths (log_file, alert_history_file, plugins_dir) at
	// the config file's directory instead of the process working directory
	// (Medium-10). static_dir is intentionally left alone: the UI is shipped in
	// the repository as ./web/static, not next to dashboard_config.json.
	cfg.resolvePaths(filepath.Dir(path))

	if err := validateDashboardConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// applyNotificationDefaults はゼロ値を既定で埋める。
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

// validateAgentConfig は agent 設定を検証する。
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
	// A-3: 署名ポリシーが有効なのに使用不能な場合は fail-closed にする。
	// A-3: fail closed when the signature policy is enabled but unusable.
	if err := cfg.Plugins.Validate(); err != nil {
		return err
	}
	if err := ValidateForwardURL(cfg.LogForward.URL); err != nil {
		return err
	}
	return nil
}

// ResolvePluginsDir は agent のプラグインディレクトリの絶対パスを返す。
// 優先順: 1. agent_config.json の plugins_dir（相対は Abs で解決）
// 2. 実行ファイル隣の plugins/ 3. フォールバックとして ./plugins。
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

// validateDashboardConfig は dashboard 設定を検証する。
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
			// 大文字小文字・空白を許容する（例: "Discord"）。これにより検証が
			// notify.FromConfig の実行時チャンネルディスパッチと一致する。
			// Accept any casing/whitespace (e.g. "Discord") so validation
			// matches the runtime channel dispatch in notify.FromConfig.
			switch strings.ToLower(strings.TrimSpace(ch.Type)) {
			case "discord":
				if ch.WebhookURL == "" {
					return fmt.Errorf("通知設定: discord には webhook_url が必須です")
				}
				// 未編集のサンプル URL が既存インストールの起動を妨げてはならない
				// （同梱 example は有効になっていた）。警告を出し、起動失敗ではなく
				// 通知側にチャンネルをスキップさせる。
				// An unedited sample URL must not brick startup on an existing
				// install (the shipped example had it enabled). Warn and let the
				// notifier skip the channel instead of failing to load.
				if IsPlaceholderWebhookURL(ch.WebhookURL) {
					fmt.Printf("[CONFIG] 警告: discord の webhook_url がサンプルのままです。このチャンネルは無効として扱われます\n")
					continue
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
				if IsPlaceholderWebhookURL(ch.WebhookURL) {
					fmt.Printf("[CONFIG] 警告: slack の webhook_url がサンプルのままです。このチャンネルは無効として扱われます\n")
					continue
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
	// A-3: 署名ポリシーが有効なのに使用不能な場合は fail-closed にする。
	// A-3: fail closed when the signature policy is enabled but unusable.
	if err := cfg.Plugins.Validate(); err != nil {
		return err
	}
	if err := ValidateForwardURL(cfg.LogForward.URL); err != nil {
		return err
	}
	return nil
}

// EnsureDir は設定ファイルのディレクトリが存在することを保証する。
// 設定ファイルは秘密を保持するため、ディレクトリは所有者のみで作成する。
// EnsureDir ensures the config file directory exists.
// Config files hold secrets, so the directory is created owner-only.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0700)
}

// boolPtr は v へのポインタを返す。省略可能な boolean 設定フィールド用だ。
// boolPtr returns a pointer to v, for optional boolean config fields.
func boolPtr(v bool) *bool { return &v }
