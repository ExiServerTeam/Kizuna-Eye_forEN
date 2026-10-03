package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/module"
)

const pluginName = "kizuna_security"

type SecurityPlugin struct {
	logger  module.Logger
	fileLog *FileLogger

	mu      sync.RWMutex
	config  *SecurityConfig
	monitor *Monitor
	fim     *FIM
	blocker *blocker
	portMon *PortMonitor
	suidMon *SUIDMonitor
	cronMon *CronMonitor

	ctx    context.Context
	cancel context.CancelFunc
}

func NewPluginModule(logger module.Logger) module.PluginModule {
	return &SecurityPlugin{logger: logger}
}

func (p *SecurityPlugin) Name() string { return pluginName }

func (p *SecurityPlugin) DisplayName() string { return "Kizuna-Security" }

func (p *SecurityPlugin) Description() string {
	return "SSHログイン・sudo・インストール・改ざん・新規ポート・SUID/SGID・cron変更・不審IPを監視するセキュリティプラグイン"
}

func (p *SecurityPlugin) Init(ctx context.Context) error {
	p.ctx, p.cancel = context.WithCancel(ctx)
	go p.integrityLoop()
	if p.logger != nil {
		p.logger.Info("Kizuna-Security プラグイン初期化")
	}
	return nil
}

func (p *SecurityPlugin) Interval() time.Duration {
	p.mu.RLock()
	cfg := p.config
	p.mu.RUnlock()
	poll := 15 * time.Second
	if cfg != nil && cfg.PollInterval > 0 {
		poll = time.Duration(cfg.PollInterval) * time.Second
	}
	if poll < time.Second {
		poll = time.Second
	}
	return poll
}

func (p *SecurityPlugin) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.RLock()
	mon := p.monitor
	fim := p.fim
	blk := p.blocker
	portMon := p.portMon
	suidMon := p.suidMon
	cronMon := p.cronMon
	p.mu.RUnlock()

	if mon != nil {
		mon.Scan()
	}
	if fim != nil {
		fim.Check()
	}
	if cronMon != nil {
		cronMon.Check()
	}
	if portMon != nil {
		portMon.Check()
	}
	if suidMon != nil {
		suidMon.Check()
	}
	if blk != nil {
		blk.UnblockExpired()
	}
	return nil
}

// disabledMarkerPath is where the plugin records that it could not start, so
// the failure is visible to an operator even though the event channel (the
// Monitor) does not exist yet. It sits next to the security log.
func disabledMarkerPath(cfg *SecurityConfig) string {
	dir := "."
	if cfg != nil && cfg.LogPath != "" {
		dir = dirOf(cfg.LogPath)
	}
	return dir + "/kizuna-security-DISABLED.json"
}

// notifyChainKeyFailure reports a startup failure that would otherwise leave
// the security monitoring silently disabled (Medium-7). The monitor — and with
// it the normal dashboard/Discord event path — is created only after the chain
// key loads, so the reason goes to the host log and to a marker file that an
// operator (or the next audit) can find.
func (p *SecurityPlugin) notifyChainKeyFailure(cfg *SecurityConfig, cause error) {
	if p.logger != nil {
		p.logger.Error("Kizuna-Security を開始できません（チェーン鍵 %s）: %v — 改ざん検知は停止します",
			cfg.ChainKeyPath, cause)
	}
	payload, err := json.MarshalIndent(map[string]interface{}{
		"ts":             time.Now().Format(time.RFC3339),
		"plugin":         pluginName,
		"reason":         cause.Error(),
		"chain_key_path": cfg.ChainKeyPath,
		"log_path":       cfg.LogPath,
		"hint":           "チェーン鍵を 0600 の通常ファイルとして復旧し、サービスを再起動してください。復旧するまで改ざん検知は停止します。",
	}, "", "  ")
	if err != nil {
		return
	}
	marker := disabledMarkerPath(cfg)
	if err := fsutil.WriteFileAtomic(marker, append(payload, '\n'), 0600); err != nil && p.logger != nil {
		p.logger.Error("無効化マーカーの書き込みに失敗 (%s): %v", marker, err)
	}
}

func (p *SecurityPlugin) Configure(config interface{}) error {
	raw, ok := config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("設定型が不正です: map[string]interface{} が必要")
	}

	cfg, err := ParseConfig(raw)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("設定検証失敗: %w", err)
	}

	fl, err := NewFileLoggerKeyed(cfg.LogPath, cfg.ChainKeyPath)
	if err != nil {
		// Medium-7: ここで失敗するとモジュールは読み込まれず、監視が「無言で」
		// 消える（ダッシュボードからはモジュールが無いようにしか見えない）。
		// イベント経路（Monitor）はまだ無いので、ホストのログと無効化マーカー
		// に理由を残してから失敗を返す。
		p.notifyChainKeyFailure(cfg, err)
		return fmt.Errorf("Kizuna-Security を開始できません（監視停止）: %w", err)
	}
	// 前回の失敗マーカーが残っていたら消す（復旧した状態を反映する）。
	_ = os.Remove(disabledMarkerPath(cfg))

	mon := NewMonitor(cfg, fl, p.logger)
	// High-5: 既存ログを検証できず退避した場合は、黙って新しいチェーンを
	// 始めず critical として通知する（改ざんの痕跡が「消えた」ように見える
	// のを防ぐ）。log 側にも chain_quarantined として残している。
	if archive, reason := fl.Quarantine(); archive != "" {
		mon.emit(module.SecurityEvent{
			Category:  "integrity",
			Level:     "critical",
			Title:     msg(cfg.Language, "integrity.quarantine.title"),
			Message:   msg(cfg.Language, "integrity.quarantine.msg", cfg.LogPath, archive, reason),
			Source:    "log_chain_quarantined",
			Timestamp: time.Now(),
		})
	}
	// F-4: ベースライン自身の改ざんを検知するため、ログチェーンと同じ鍵で
	// 署名する（鍵が無ければ鍵なし SHA-256）。鍵は読み込み前に渡す。あとから
	// SetChainKey すると、鍵なしで読んだ時点の署名検証が必ず失敗し、再起動の
	// たびに「ベースライン改ざん」の誤報が出る。
	fim := NewFIMKeyed(cfg.IntegrityFiles, cfg.IntegrityBaselinePath, p.logger, mon.emit, readChainKey(cfg.ChainKeyPath))
	fim.SetLang(cfg.Language)
	blk := newBlocker(cfg, p.logger, mon.emit)
	mon.onBlockCandidate = blk.HandleBlockCandidate

	var portMon *PortMonitor
	if cfg.ListenPortCheck {
		portMon = NewPortMonitor(cfg.ListenPortBaselinePath, p.logger, mon.emit)
	}
	var suidMon *SUIDMonitor
	if cfg.SUIDCheck {
		suidMon = NewSUIDMonitor(cfg.SUIDPaths, cfg.SUIDBaselinePath, time.Duration(cfg.SUIDScanInterval)*time.Second, cfg.SUIDScanHour, cfg.Language, p.logger, mon.emit)
	}
	var cronMon *CronMonitor
	if cfg.CronCheck {
		cronMon = NewCronMonitor(cfg.CronPaths, cfg.CronBaselinePath, cfg.Language, p.logger, mon.emit)
	}

	p.mu.Lock()
	if p.fileLog != nil {
		_ = p.fileLog.Close()
	}
	p.config = cfg
	p.fileLog = fl
	// Reconfigure: stop the previous journald spoof watch, if any.
	if p.monitor != nil {
		p.monitor.Stop()
	}
	p.monitor = mon
	// Configure runs before Init, so p.ctx may be nil; pass a safe
	// background context and rely on Stop() for cancellation.
	mon.startSpoofWatch(context.Background())
	p.fim = fim
	p.blocker = blk
	p.portMon = portMon
	p.suidMon = suidMon
	p.cronMon = cronMon
	p.mu.Unlock()

	fl.Info("configured", "設定を反映しました", map[string]interface{}{
		"watch_files":       cfg.WatchFiles,
		"poll_interval_sec": cfg.PollInterval,
		"failed_burst":      cfg.FailedBurst,
		"notify_min_level":  cfg.NotifyMinimal,
		"language":          cfg.Language,
		"integrity_files":   len(cfg.IntegrityFiles),
		"block_mode":        cfg.BlockMode,
		"firewall_backend":  blk.fw.Name(),
		"listen_port_check": cfg.ListenPortCheck,
		"suid_check":        cfg.SUIDCheck,
		"cron_check":        cfg.CronCheck,
		"ssh_login_burst":   cfg.SSHLoginBurst,
	})

	if p.logger != nil {
		p.logger.Info("Kizuna-Security 設定完了: 監視ファイル=%v 改ざん監視=%d件 block=%s/%s 間隔=%ds lang=%s ポート=%v SUID=%v cron=%v",
			cfg.WatchFiles, len(cfg.IntegrityFiles), cfg.BlockMode, blk.fw.Name(), cfg.PollInterval, cfg.Language,
			cfg.ListenPortCheck, cfg.SUIDCheck, cfg.CronCheck)
	}
	return nil
}

func (p *SecurityPlugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Lock()
	if p.fileLog != nil {
		_ = p.fileLog.Close()
	}
	p.mu.Unlock()
	if p.logger != nil {
		p.logger.Info("Kizuna-Security プラグイン停止")
	}
	return nil
}

func (p *SecurityPlugin) Health(ctx context.Context) error { return nil }

func (p *SecurityPlugin) HealthInterval() time.Duration { return 60 * time.Second }

func (p *SecurityPlugin) Shutdown(ctx context.Context) error { return p.Stop() }

func (p *SecurityPlugin) DrainSecurityEvents() []module.SecurityEvent {
	p.mu.RLock()
	mon := p.monitor
	p.mu.RUnlock()
	if mon == nil {
		return nil
	}
	return mon.Drain()
}

func (p *SecurityPlugin) RequeueSecurityEvents(events []module.SecurityEvent) {
	p.mu.RLock()
	mon := p.monitor
	p.mu.RUnlock()
	if mon == nil {
		return
	}
	mon.Requeue(events)
}

func (p *SecurityPlugin) GetConfigFields() []module.ConfigField {
	minPoll := 5.0
	maxPoll := 3600.0
	minBurst := 1.0
	maxBurst := 1000.0
	minWindow := 5.0
	maxWindow := 86400.0
	minBlockDur := 60.0
	maxBlockDur := 604800.0
	minScan := 60.0
	maxScan := 86400.0
	minHour := -1.0
	maxHour := 23.0

	return []module.ConfigField{
		{Key: "watch_files", Label: "監視するログファイル", Type: module.FieldTextarea,
			Default: "/var/log/auth.log,/var/log/dpkg.log,/var/log/apt/history.log", Required: true, Group: "基本設定",
			Hint: "監視するログをカンマ区切りまたは改行で指定します。auth.log(または secure)は SSH/sudo、dpkg.log/apt・yum/dnf はインストールを検知します。"},
		{Key: "poll_interval_sec", Label: "チェック間隔（秒）", Type: module.FieldNumber,
			Default: "15", Min: &minPoll, Max: &maxPoll, Group: "基本設定",
			Hint: "ログ読み取りとファイル改ざんチェックの間隔です（5〜3600秒）。"},
		{Key: "language", Label: "通知メッセージの言語", Type: module.FieldSelect,
			Options: []string{"ja", "en"}, Default: "ja", Group: "通知",
			Hint: "検知メッセージを日本語(ja)または英語(en)で出力します。"},
		{Key: "notify_min_level", Label: "通知する最小レベル", Type: module.FieldSelect,
			Options: []string{"critical", "warning", "info"}, Default: "warning", Group: "通知",
			Hint: "このレベル以上のイベントを Discord とダッシュボードに通知します。info は履歴のみに残します。"},
		{Key: "failed_burst", Label: "SSH失敗の多発しきい値（回）", Type: module.FieldNumber,
			Default: "5", Min: &minBurst, Max: &maxBurst, Group: "検知",
			Hint: "同一IPからのログイン失敗がこの回数に達したら「重大」として扱います。"},
		{Key: "burst_window_sec", Label: "失敗回数を数える時間枠（秒）", Type: module.FieldNumber,
			Default: "60", Min: &minWindow, Max: &maxWindow, Group: "検知",
			Hint: "この時間内の失敗回数で多発を判定します。"},
		{Key: "ssh_login_burst", Label: "SSHログイン成功の急増しきい値（回）", Type: module.FieldNumber,
			Default: "10", Min: &minBurst, Max: &maxBurst, Group: "検知",
			Hint: "同一IPからのログイン成功がこの回数に達したら「重大」として通知します。"},
		{Key: "ssh_login_window_sec", Label: "ログイン成功を数える時間枠（秒）", Type: module.FieldNumber,
			Default: "300", Min: &minWindow, Max: &maxWindow, Group: "検知",
			Hint: "この時間内の成功回数で急増を判定します。"},
		{Key: "listen_port_check", Label: "新規リッスンポートを検知する", Type: module.FieldCheckbox,
			Default: "true", Group: "検知",
			Hint: "ss で待ち受けポートを監視し、新たに出現したポートを通知します。"},
		{Key: "suid_check", Label: "SUID/SGID ファイルを検知する", Type: module.FieldCheckbox,
			Default: "true", Group: "検知",
			Hint: "指定ディレクトリを定期的に走査し、新たな SUID/SGID ファイルを通知します。"},
		{Key: "suid_paths", Label: "SUID/SGID を走査するディレクトリ", Type: module.FieldTextarea,
			Default: "/usr/bin,/usr/sbin,/bin,/sbin,/usr/local/bin,/usr/local/sbin", Group: "検知",
			Hint: "カンマ区切りまたは改行で指定します。"},
		{Key: "suid_scan_interval_sec", Label: "SUID/SGID スキャン間隔（秒）", Type: module.FieldNumber,
			Default: "3600", Min: &minScan, Max: &maxScan, Group: "検知",
			Hint: "走査は重いため、既定は1時間です（60〜86400秒）。"},
		{Key: "suid_scan_hour", Label: "SUID/SGID を走査する時刻（0-23時）", Type: module.FieldNumber,
			Default: "3", Min: &minHour, Max: &maxHour, Group: "検知",
			Hint: "この時刻台にだけ走査します（既定は深夜3時）。-1 で時間帯制限なし。走査は nice/ionice で低優先度実行します。"},
		{Key: "cron_check", Label: "cron の変更を検知する", Type: module.FieldCheckbox,
			Default: "true", Group: "検知",
			Hint: "cron 関連ファイルの追加・変更・削除を通知します。"},
		{Key: "cron_paths", Label: "cron 監視パス", Type: module.FieldTextarea,
			Default: "/etc/crontab,/etc/cron.d,/etc/cron.daily,/etc/cron.hourly,/etc/cron.weekly,/etc/cron.monthly,/var/spool/cron,/var/spool/cron/crontabs", Group: "検知",
			Hint: "カンマ区切りまたは改行で指定します。"},
		{Key: "sudo_ignore_commands", Label: "sudo 通知から除外するコマンド", Type: module.FieldTextarea,
			Default: cronSudoHelper + ",/usr/sbin/smartctl,smartctl", Group: "検知",
			Hint: "指定した接頭辞で始まるコマンドは sudo 実行通知を出しません。読み取り専用の自作ヘルパー（cron 監視用）は常に対象外です。"},
		{Key: "integrity_files", Label: "改ざん監視する重要ファイル", Type: module.FieldTextarea,
			Default: "/etc/passwd,/etc/shadow,/etc/group,/etc/sudoers,/etc/ssh/sshd_config,/etc/ld.so.preload,/etc/crontab,/etc/hosts,/root/.ssh/authorized_keys",
			Group:   "ファイル完全性監視",
			Hint:    "絶対パスで指定します。変更・作成・削除を検知して通知します。監視対象に後から追加したファイルも通知します。"},
		{Key: "integrity_baseline_path", Label: "ベースラインの保存先", Type: module.FieldText,
			Default: "./logs/kizuna-security-fim.json", Group: "ファイル完全性監視",
			Hint: "各ファイルのハッシュを記録するファイルです。"},
		{Key: "block_mode", Label: "不審IPの自動ブロック", Type: module.FieldSelect,
			Options: []string{"off", "dry-run", "enforce"}, Default: "off", Group: "自動ブロック",
			Hint: "off=無効 / dry-run=ブロックせず通知のみ / enforce=実際にブロック（要 sudo -n）。"},
		{Key: "firewall_backend", Label: "ブロックに使うファイアウォール", Type: module.FieldSelect,
			Options: []string{"auto", "iptables", "nftables", "firewalld"}, Default: "auto", Group: "自動ブロック",
			Hint: "auto は firewalld（稼働中）→ nftables → iptables の順に自動判定します。"},
		{Key: "block_duration_sec", Label: "ブロックを解除するまでの秒数", Type: module.FieldNumber,
			Default: "600", Min: &minBlockDur, Max: &maxBlockDur, Group: "自動ブロック",
			Hint: "enforce 時にこの時間だけブロックし、自動解除します。"},
		{Key: "block_state_path", Label: "ブロック状態の保存先", Type: module.FieldText,
			Default: "./logs/kizuna-security-blocks.json", Group: "自動ブロック",
			Hint: "再起動時にブロック状態を復元するためのファイルです。"},
		{Key: "block_whitelist", Label: "絶対にブロックしないIP", Type: module.FieldTextarea,
			Default: "", Group: "自動ブロック",
			Hint: "管理者PCなど、誤ってブロックしたくない IP / CIDR をカンマ区切りで指定します。"},
		{Key: "log_path", Label: "検知ログの保存先", Type: module.FieldText,
			Default: "./logs/kizuna-security.log", Group: "詳細設定",
			Hint: "検知したイベントを JSON Lines で記録します（ハッシュチェーン付き）。"},
	}
}

var Plugin = &SecurityPlugin{}

func main() {
	_ = Plugin
	_ = Plugin.GetConfigFields
	_ = Plugin.DisplayName
}

// integrityLoop periodically verifies the security-log hash chain and the
// alert_history consistency. It reads the interval from the live config so a
// config change takes effect without a restart.
func (p *SecurityPlugin) integrityLoop() {
	first := time.NewTimer(30 * time.Second)
	defer first.Stop()
	select {
	case <-p.ctx.Done():
		return
	case <-first.C:
		p.runIntegrityChecks()
	}
	for {
		p.mu.RLock()
		interval := 300
		if p.config != nil && p.config.IntegrityCheckInterval > 0 {
			interval = p.config.IntegrityCheckInterval
		}
		p.mu.RUnlock()
		t := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-p.ctx.Done():
			t.Stop()
			return
		case <-t.C:
			p.runIntegrityChecks()
		}
	}
}
