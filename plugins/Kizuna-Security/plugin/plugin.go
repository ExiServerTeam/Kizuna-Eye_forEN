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

	mu          sync.RWMutex
	config      *SecurityConfig
	monitor     *Monitor
	fim         *FIM
	blocker     *blocker
	portMon     *PortMonitor
	suidMon     *SUIDMonitor
	suidFastMon *SUIDMonitor
	// suidWatch is the inotify watch that turns a directory change under the
	// high-risk paths into an immediate SUID scan. nil when disabled.
	suidWatch *suidWatcher
	// fimDir holds the directory-monitoring hashes (fim_watch) and fimWatch is
	// the inotify layer that feeds it the changed paths. Both nil when disabled.
	fimDir   *FIMDirWatcher
	fimWatch *suidWatcher
	// fastStarted guards the one-time start of the fast SUID loop (Run is called
	// on every agent poll).
	fastStarted bool
	// fimLoopStarted guards the one-time start of the directory watch loop.
	fimLoopStarted bool
	// watchErrorLogged keeps a broken inotify setup from logging on every poll.
	watchErrorLogged bool
	// fimWatchErrorLogged is the same, for the directory watch.
	fimWatchErrorLogged bool
	cronMon             *CronMonitor

	ctx    context.Context
	cancel context.CancelFunc
}

func NewPluginModule(logger module.Logger) module.PluginModule {
	return &SecurityPlugin{logger: logger}
}

func (p *SecurityPlugin) Name() string { return pluginName }

func (p *SecurityPlugin) DisplayName() string { return "Kizuna-Security" }

// Tag is the short badge shown on the plugin card. Kizuna-Security is a
// guard-class plugin, so it declares "GUARD".
func (p *SecurityPlugin) Tag() string { return "GUARD" }

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
	suidFastMon := p.suidFastMon
	fimDir := p.fimDir
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
	// 高リスクディレクトリ（/tmp 等）の SUID/SGID は、時間帯制限なしの走査
	// に加えて inotify で即時検知する。周期走査だけでは「走査と走査の間に
	// 作成〜削除が完結したファイル」を原理的に見られないため。
	if suidFastMon != nil {
		p.startFastSUIDLoop(ctx)
		p.ensureSUIDWatch()
	}
	// ディレクトリ監視もエージェントのポーリング間隔に縛られない専用ループで
	// 回す（Run は毎ポーリング呼ばれるので、開始は一度だけ）。
	if fimDir != nil {
		p.startFIMWatchLoop(ctx)
		p.ensureFIMWatch()
	}
	if blk != nil {
		blk.UnblockExpired()
	}
	return nil
}

// fastSUIDBackstopInterval returns how often the high-risk paths are scanned
// even when the event-driven watch is active. It is 0 when the fast monitor is
// not configured.
func (p *SecurityPlugin) fastSUIDBackstopInterval() time.Duration {
	p.mu.RLock()
	mon := p.suidFastMon
	p.mu.RUnlock()
	if mon == nil {
		return 0
	}
	d := mon.Interval()
	if d <= 0 {
		// 間隔が未設定でも監視を止めない（既定 10 秒）。
		return 10 * time.Second
	}
	return d
}

// startFastSUIDLoop starts the scan loop that is independent of the agent poll
// interval. Run is called on every poll, so the loop is started at most once.
func (p *SecurityPlugin) startFastSUIDLoop(runCtx context.Context) {
	p.mu.Lock()
	if p.fastStarted {
		p.mu.Unlock()
		return
	}
	p.fastStarted = true
	base := p.ctx
	p.mu.Unlock()
	if base == nil {
		// Configure/Init order changes could leave p.ctx nil; the Run context is
		// cancelled on shutdown too.
		base = runCtx
	}
	go p.fastSUIDLoop(base)
}

// fastSUIDLoop scans the high-risk paths on its own schedule.
//
// It must not be driven by the agent poll interval: with the default 15s poll a
// 10s interval is quantised to 15s, and a SUID file that lives for 10s then
// disappears is missed about once in three attempts (measured: 2 misses in 7 on
// a pentest run). The interval is re-read every cycle so a config change takes
// effect without a restart.
func (p *SecurityPlugin) fastSUIDLoop(ctx context.Context) {
	for {
		interval := p.fastSUIDBackstopInterval()
		if interval <= 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}

		p.mu.RLock()
		mon := p.suidFastMon
		p.mu.RUnlock()
		if mon == nil {
			return
		}
		// inotify が落ちていたら（fd 枯渇などの一時障害）張り直す。
		p.ensureSUIDWatch()
		mon.CheckForce()
	}
}

// ensureSUIDWatch starts the inotify watch when it is configured but not
// running. A failure is not fatal: the periodic scan keeps covering the paths,
// so the only thing lost is the sub-scan-interval window.
func (p *SecurityPlugin) ensureSUIDWatch() {
	p.mu.RLock()
	w := p.suidWatch
	p.mu.RUnlock()
	if w == nil || w.Running() {
		return
	}
	err := w.start()
	if err == nil {
		p.mu.Lock()
		p.watchErrorLogged = false
		p.mu.Unlock()
		return
	}
	if p.logger == nil {
		return
	}
	p.mu.Lock()
	logged := p.watchErrorLogged
	p.watchErrorLogged = true
	p.mu.Unlock()
	if logged {
		p.logger.Debug("Kizuna-Security SUID: inotify を再開できません: %v", err)
		return
	}
	p.logger.Warn("Kizuna-Security SUID: inotify を開始できません (%v)。高リスク領域は周期走査 (%s) で検知します。", err, p.fastSUIDBackstopInterval())
}

// startFIMWatchLoop starts the fallback scan loop of the directory watch. Run
// is called on every poll, so the loop is started at most once.
func (p *SecurityPlugin) startFIMWatchLoop(runCtx context.Context) {
	p.mu.Lock()
	if p.fimLoopStarted {
		p.mu.Unlock()
		return
	}
	p.fimLoopStarted = true
	base := p.ctx
	p.mu.Unlock()
	if base == nil {
		base = runCtx
	}
	go p.fimWatchLoop(base)
}

// fimDirInterval returns the fallback scan cadence of the directory watch
// (0 when the watch is disabled).
func (p *SecurityPlugin) fimDirInterval() time.Duration {
	p.mu.RLock()
	dir := p.fimDir
	p.mu.RUnlock()
	if dir == nil {
		return 0
	}
	return dir.Interval()
}

// fimWatchLoop is the fallback for the directory watch. It re-scans on its own
// schedule (so a raised agent poll interval does not widen the blind spot) and
// re-installs the inotify watch if it broke (fd exhaustion etc.). The primary
// path is the event-driven one; this is what keeps the monitoring correct when
// inotify is unavailable.
func (p *SecurityPlugin) fimWatchLoop(ctx context.Context) {
	for {
		interval := p.fimDirInterval()
		if interval <= 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}

		p.mu.RLock()
		dir := p.fimDir
		p.mu.RUnlock()
		if dir == nil {
			return
		}
		p.ensureFIMWatch()
		dir.Check()
	}
}

// ensureFIMWatch starts the directory watch when it is configured but not
// running. A failure is not fatal: the fallback scan keeps covering the paths,
// so the only thing lost is the sub-interval window.
func (p *SecurityPlugin) ensureFIMWatch() {
	p.mu.RLock()
	w := p.fimWatch
	p.mu.RUnlock()
	if w == nil || w.Running() {
		return
	}
	err := w.start()
	if err == nil {
		p.mu.Lock()
		p.fimWatchErrorLogged = false
		p.mu.Unlock()
		return
	}
	if p.logger == nil {
		return
	}
	p.mu.Lock()
	logged := p.fimWatchErrorLogged
	p.fimWatchErrorLogged = true
	p.mu.Unlock()
	if logged {
		p.logger.Debug("Kizuna-Security FIM: inotify を再開できません: %v", err)
		return
	}
	p.logger.Warn("Kizuna-Security FIM: inotify を開始できません (%v)。監視ディレクトリは周期走査 (%s) で検知します。", err, p.fimDirInterval())
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
		mon.emit(i18nEvent(cfg.Language, "integrity", "critical", "integrity.quarantine.title", "integrity.quarantine.msg",
			module.SecurityEvent{Source: "log_chain_quarantined", Timestamp: time.Now()}, cfg.LogPath, archive, reason))
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
		portMon = NewPortMonitorWithCooldown(cfg.ListenPortBaselinePath, cfg.ListenPortRenotifyCooldownSec, cfg.Language, p.logger, mon.emit)
	}
	var suidMon *SUIDMonitor
	if cfg.SUIDCheck {
		suidMon = NewSUIDMonitor(cfg.SUIDPaths, cfg.SUIDBaselinePath, time.Duration(cfg.SUIDScanInterval)*time.Second, cfg.SUIDScanHour, cfg.Language, p.logger, mon.emit)
	}
	// 高リスクディレクトリ用の第二の SUID 監視。scanHour=-1（時間帯制限なし）。
	var suidFastMon *SUIDMonitor
	if cfg.SUIDFastCheck {
		suidFastMon = NewSUIDMonitor(cfg.SUIDFastPaths, cfg.SUIDFastBaselinePath, time.Duration(cfg.SUIDFastScanInterval)*time.Second, -1, cfg.Language, p.logger, mon.emit)
	}
	// 動きの速い置き場（/tmp 等）は inotify でも監視し、ディレクトリの変化を
	// 契機に即時走査する。周期走査の間隔内で作成〜削除が完結する攻撃を拾う。
	var suidWatch *suidWatcher
	if suidFastMon != nil && cfg.SUIDWatch {
		fast := suidFastMon
		suidWatch = newSUIDWatcher(cfg.SUIDWatchPaths, p.logger, func() { fast.CheckForce() })
	}
	var cronMon *CronMonitor
	if cfg.CronCheck {
		cronMon = NewCronMonitor(cfg.CronPaths, cfg.CronBaselinePath, cfg.Language, p.logger, mon.emit)
	}
	// ディレクトリ監視（fim_watch）。IntegrityFiles は事前に列挙した絶対パス
	// しか見ないため、攻撃者が任意の名前でファイルを作る置き場（/tmp 等）は
	// 検知できない（攻撃 A-5 の回帰）。inotify が知らせたパスをその場で
	// ハッシュし、作成・変更・削除を検知する。
	var fimDir *FIMDirWatcher
	var fimWatch *suidWatcher
	if cfg.FIMWatch && len(cfg.FIMWatchPaths) > 0 {
		fimDir = NewFIMDirWatcher(cfg.FIMWatchPaths, cfg.FIMWatchIgnore, cfg.FIMWatchBaselinePath,
			cfg.FIMWatchInterval, cfg.FIMWatchMaxFiles, cfg.FIMWatchMaxSizeKB,
			p.logger, mon.emit, readChainKey(cfg.ChainKeyPath),
			WithFIMDirMaxDepth(cfg.FIMWatchMaxDepth),
			WithFIMHeadBytes(cfg.FIMWatchHeadBytes))
		fimDir.SetLang(cfg.Language)
		// label はログの識別子（SUID 監視と区別する）。
		fimWatch = newInotifyWatcher(cfg.FIMWatchPaths, "FIM", p.logger, fimDir.HandleChanges,
			withMaxDepth(cfg.FIMWatchMaxDepth),
			withMaxDirsPerRoot(cfg.FIMWatchMaxDirs, fimDir.WarnWatchDirs))
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
	p.suidFastMon = suidFastMon
	prevWatch := p.suidWatch
	p.suidWatch = suidWatch
	watchLoopRunning := p.fastStarted
	prevFimWatch := p.fimWatch
	p.fimWatch = fimWatch
	fimLoopRunning := p.fimLoopStarted
	p.fimDir = fimDir
	p.cronMon = cronMon
	p.mu.Unlock()

	// 設定変更で監視対象が変わった場合、古い inotify 監視を残さない。
	if prevWatch != nil && prevWatch != suidWatch {
		prevWatch.stop()
	}
	if prevFimWatch != nil && prevFimWatch != fimWatch {
		prevFimWatch.stop()
	}
	// 走査ループが既に動いているなら、新しい監視をその場で張り直す
	// （次回の Run まで待つと、その間だけ即時検知が抜ける）。
	if watchLoopRunning && suidWatch != nil {
		p.ensureSUIDWatch()
	}
	if fimLoopRunning && fimDir != nil {
		p.ensureFIMWatch()
	}

	fl.Info("configured", "設定を反映しました", map[string]interface{}{
		"watch_files":            cfg.WatchFiles,
		"poll_interval_sec":      cfg.PollInterval,
		"failed_burst":           cfg.FailedBurst,
		"notify_min_level":       cfg.NotifyMinimal,
		"language":               cfg.Language,
		"integrity_files":        len(cfg.IntegrityFiles),
		"block_mode":             cfg.BlockMode,
		"firewall_backend":       blk.fw.Name(),
		"listen_port_check":      cfg.ListenPortCheck,
		"suid_check":             cfg.SUIDCheck,
		"suid_fast_check":        cfg.SUIDFastCheck,
		"suid_fast_paths":        cfg.SUIDFastPaths,
		"suid_fast_interval_sec": cfg.SUIDFastScanInterval,
		"suid_watch":             cfg.SUIDWatch,
		"suid_watch_paths":       cfg.SUIDWatchPaths,
		"fim_watch":              cfg.FIMWatch,
		"fim_watch_paths":        cfg.FIMWatchPaths,
		"cron_check":             cfg.CronCheck,
		"ssh_login_burst":        cfg.SSHLoginBurst,
	})

	if p.logger != nil {
		p.logger.Info("Kizuna-Security 設定完了: 監視ファイル=%v 改ざん監視=%d件 block=%s/%s 間隔=%ds lang=%s ポート=%v SUID=%v 高速SUID=%v/%ds inotify=%v FIM監視=%v/%v cron=%v",
			cfg.WatchFiles, len(cfg.IntegrityFiles), cfg.BlockMode, blk.fw.Name(), cfg.PollInterval, cfg.Language,
			cfg.ListenPortCheck, cfg.SUIDCheck, cfg.SUIDFastCheck, cfg.SUIDFastScanInterval, cfg.SUIDWatch,
			cfg.FIMWatch, cfg.FIMWatchPaths, cfg.CronCheck)
	}
	return nil
}

func (p *SecurityPlugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Lock()
	w := p.suidWatch
	p.suidWatch = nil
	fw := p.fimWatch
	p.fimWatch = nil
	p.fimDir = nil
	if p.fileLog != nil {
		_ = p.fileLog.Close()
	}
	p.mu.Unlock()
	if w != nil {
		// inotify の fd と読み取り goroutine を解放する。
		w.stop()
	}
	if fw != nil {
		fw.stop()
	}
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
	minFastScan := 5.0
	maxFastScan := 3600.0
	minHour := -1.0
	maxHour := 23.0
	minFimFiles := 16.0
	maxFimFiles := 1000000.0
	minFimSize := 1.0
	maxFimSize := 1048576.0

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
		{Key: "suid_fast_check", Label: "高リスク領域(/tmp 等)の SUID/SGID を常時検知する", Type: module.FieldCheckbox,
			Default: "true", Group: "検知",
			Hint: "攻撃者が SUID バイナリを置きやすい /tmp・/var/tmp・/dev/shm・/home 等を、時間帯制限なしで短い間隔で走査します。"},
		{Key: "suid_fast_paths", Label: "高リスク領域の走査パス", Type: module.FieldTextarea,
			Default: "/tmp,/var/tmp,/dev/shm,/run,/home,/opt,/srv", Group: "検知",
			Hint: "カンマ区切りまたは改行で指定します。大きすぎるディレクトリを入れると負荷が上がります。"},
		{Key: "suid_fast_interval_sec", Label: "高リスク領域の走査間隔（秒）", Type: module.FieldNumber,
			Default: "10", Min: &minFastScan, Max: &maxFastScan, Group: "検知",
			Hint: "既定は10秒です（5〜3600秒）。この走査はエージェントのポーリング間隔に縛られず、専用ループで動きます。短いほど即時性は上がりますが負荷も上がります。"},
		{Key: "suid_watch", Label: "高リスク領域を inotify で即時監視する", Type: module.FieldCheckbox,
			Default: "true", Group: "検知",
			Hint: "SUID/SGID ファイルの作成・改名・権限変更を inotify で即座に検知します。周期走査の合間で消えてしまう短命なファイルも拾えます（Linux のみ。使えない場合は周期走査のみになります）。"},
		{Key: "suid_watch_paths", Label: "inotify で即時監視するパス", Type: module.FieldTextarea,
			Default: "/tmp,/var/tmp,/dev/shm,/run", Group: "検知",
			Hint: "動きが速く攻撃者が実際に置く置き場を指定します。巨大なツリー（/home 全体など）を入れると inotify のホスト全体の監視上限を消費するため、必要な場所だけにしてください（最大64件）。"},
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
		{Key: "fim_watch", Label: "監視ディレクトリを inotify で即時監視する", Type: module.FieldCheckbox,
			Default: "false", Group: "ファイル完全性監視",
			Hint: "integrity_files は事前に列挙した絶対パスしか見ないため、/tmp のような「攻撃者が任意の名前でファイルを置く場所」を検知できません。ここに挙げたディレクトリ配下を inotify で監視し、作成・変更・削除を検知します（Linux のみ。使えない場合は周期走査のみになります）。"},
		{Key: "fim_watch_paths", Label: "監視するディレクトリ", Type: module.FieldTextarea,
			Default: "/tmp,/var/tmp,/dev/shm", Group: "ファイル完全性監視",
			Hint: "カンマ区切りまたは改行で指定します。ビジーなディレクトリを丸ごと入れると正規の一時ファイルでも通知が出るため、対象を絞るか fim_watch_ignore と併用してください（最大64件・深さ3階層）。"},
		{Key: "fim_watch_interval_sec", Label: "監視ディレクトリの再走査間隔（秒）", Type: module.FieldNumber,
			Default: "10", Min: &minFastScan, Max: &maxFastScan, Group: "ファイル完全性監視",
			Hint: "inotify が使えない環境・通知を取りこぼした場合のフォールバック間隔です（5〜3600秒）。通常の検知はイベント駆動で即時です。"},
		{Key: "fim_watch_max_files", Label: "1回の走査でハッシュする最大ファイル数", Type: module.FieldNumber,
			Default: "4096", Min: &minFimFiles, Max: &maxFimFiles, Group: "ファイル完全性監視",
			Hint: "監視ディレクトリが巨大な場合の負荷上限です。上限を超えた分は走査されず、その旨を警告します（16〜1000000件）。"},
		{Key: "fim_watch_max_size_kb", Label: "ハッシュする最大ファイルサイズ（KB）", Type: module.FieldNumber,
			Default: "4096", Min: &minFimSize, Max: &maxFimSize, Group: "ファイル完全性監視",
			Hint: "これより大きいファイルは内容をハッシュしません（1回の走査が巨大ファイルで固まるのを防ぐため。1〜1048576KB）。"},
		{Key: "fim_watch_ignore", Label: "監視ディレクトリで除外するパターン", Type: module.FieldTextarea,
			Default: "", Group: "ファイル完全性監視",
			Hint: "glob で指定します（例: *.swp,/tmp/systemd-private-*）。ファイル名とパスの両方に照合します（最大64件）。"},
		{Key: "fim_watch_baseline_path", Label: "監視ディレクトリのベースライン保存先", Type: module.FieldText,
			Default: "./logs/kizuna-security-fim-dirs.json", Group: "ファイル完全性監視",
			Hint: "各ファイルのハッシュを記録するファイルです（署名付き）。"},
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
