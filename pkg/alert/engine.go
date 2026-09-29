package alert

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/notify"
	"Kizuna-Eye/pkg/status"
)

// ============================================================
// Config is the alert engine configuration.
// ============================================================
type Config struct {
	MemoryWarn       float64
	MemoryCritical   float64
	DiskFreeWarn     float64
	DiskFreeCritical float64
	CPUTempWarn      float64
	CPUTempCritical  float64

	HoldDuration time.Duration // how long an anomaly must persist
	RecoveryHold time.Duration // normal duration required to recover (hysteresis)
	Cooldown     time.Duration // cooldown before re-notifying the same alert
	AgentTimeout time.Duration // agent timeout

	NotifyRecovery bool
}

// metricState is the per-metric state.
type metricState struct {
	startedAt    time.Time
	normalSince  time.Time
	lastNotified time.Time
	firedLevel   notify.Level
}

// ============================================================
// Engine detects alerts and sends notifications.
// ============================================================
type Engine struct {
	cfg      Config
	log      *logger.Logger
	notifier *notify.Manager

	mu     sync.Mutex
	states map[string]*metricState

	agentOnline        bool
	agentOfflineReason string
	agentLastSeen      time.Time
	// agentOfflineNotified is the last time an agent-offline notification was
	// sent. It throttles a flapping connection so 切断/復帰 pairs do not spam
	// the notification channels.
	agentOfflineNotified time.Time

	history *History
}

func NewEngine(cfg Config, notifier *notify.Manager, log *logger.Logger) *Engine {
	return &Engine{
		cfg:      cfg,
		log:      log,
		notifier: notifier,
		states:   make(map[string]*metricState),
		history:  NewHistory(200),
	}
}

// Alerts returns the alert history, newest first.
func (e *Engine) Alerts() []HistoryEntry {
	if e.history == nil {
		return nil
	}
	return e.history.List()
}

// Snapshot returns the current thresholds (thread-safe).
func (e *Engine) Snapshot() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// UpdateConfig replaces runtime-adjustable thresholds (thread-safe).
func (e *Engine) UpdateConfig(c Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Preserve durations that are not editable via the API.
	old := e.cfg
	if c.HoldDuration == 0 {
		c.HoldDuration = old.HoldDuration
	}
	if c.Cooldown == 0 {
		c.Cooldown = old.Cooldown
	}
	if c.RecoveryHold == 0 {
		c.RecoveryHold = old.RecoveryHold
	}
	if c.AgentTimeout == 0 {
		c.AgentTimeout = old.AgentTimeout
	}
	e.cfg = c
}

// EnableHistoryPersistence loads prior history and persists new entries.
func (e *Engine) EnableHistoryPersistence(path string) {
	if e.history == nil {
		return
	}
	if e.log != nil {
		e.history.SetLogger(e.log.Warn)
	}
	if err := e.history.Load(path); err != nil && e.log != nil {
		e.log.Warn("アラート履歴の読み込み失敗: %v", err)
	}
	e.history.SetPersistence(path)
}

// ClearAlerts removes every alert history record (memory and file).
// It does not reset the per-metric fired state, so clearing the history
// never triggers a duplicate notification.
func (e *Engine) ClearAlerts() error {
	if e.history == nil {
		return nil
	}
	return e.history.Clear()
}

// ReportAlert records an externally supplied alert to history and notifies.
// Used by plugins (e.g. security) that detect their own events.
func (e *Engine) ReportAlert(a *notify.Alert) {
	e.dispatch(a)
}

// dispatch records to history and then notifies.
func (e *Engine) dispatch(a *notify.Alert) {
	if a == nil {
		return
	}
	if e.history != nil {
		e.history.Add(a)
	}
	if e.notifier != nil {
		e.notifier.Notify(a)
	}
}

// ============================================================
// OnStatus is called when new data arrives from the agent.
// ============================================================
func (e *Engine) OnStatus(s *status.SystemStatus) {
	if s == nil {
		return
	}
	var pending []*notify.Alert
	now := time.Now()

	e.mu.Lock()

	// ---------- Agent recovery ----------
	if !e.agentOnline {
		if e.agentOfflineReason != "" && e.cfg.NotifyRecovery {
			pending = append(pending, &notify.Alert{
				Type:      "agent_recovery",
				Level:     notify.LevelSuccess,
				Icon:      iconForLevel(notify.LevelSuccess),
				Title:     "Agent 復帰",
				Message:   "Agentからの応答が復帰しました。",
				Timestamp: now,
			})
		}
		e.agentOnline = true
		e.agentOfflineReason = ""
	}
	e.agentLastSeen = now

	// ---------- Memory ----------
	if a := e.evalMemory(s.MemPercent, now); a != nil {
		pending = append(pending, a)
	}

	// ---------- Storage ----------
	freeBytes := uint64(0)
	if s.DiskTotal > s.DiskUsed {
		freeBytes = s.DiskTotal - s.DiskUsed
	}
	diskFreePct := 100.0 - s.DiskPercent
	// Clamp against slightly out-of-range DiskPercent (rounding, odd drivers).
	if diskFreePct < 0 {
		diskFreePct = 0
	} else if diskFreePct > 100 {
		diskFreePct = 100
	}
	if a := e.evalDiskFree(diskFreePct, freeBytes, now); a != nil {
		pending = append(pending, a)
	}

	// ---------- CPU temperature ----------
	if a := e.evalCPUTemp(s.CPUTemp, now); a != nil {
		pending = append(pending, a)
	}

	// ---------- Disk S.M.A.R.T ----------
	if a := e.evalDiskHealth(s.Disks, now); a != nil {
		pending = append(pending, a)
	}

	e.mu.Unlock()

	for _, a := range pending {
		e.dispatch(a)
	}
}

// ============================================================
// CheckAgentTimeout is called periodically.
// ============================================================
func (e *Engine) CheckAgentTimeout() {
	e.mu.Lock()
	if !e.agentOnline || e.agentLastSeen.IsZero() {
		e.mu.Unlock()
		return
	}
	if time.Since(e.agentLastSeen) < e.cfg.AgentTimeout {
		e.mu.Unlock()
		return
	}
	e.agentOnline = false
	if e.allowAgentOfflineNotifyLocked(time.Now()) {
		e.agentOfflineReason = "timeout"
		e.mu.Unlock()

		e.dispatch(&notify.Alert{
			Type:      "agent_timeout",
			Level:     notify.LevelWarning,
			Icon:      iconForLevel(notify.LevelWarning),
			Title:     "Agent 応答なし",
			Message:   "Agentから応答がありません。至急確認してください。",
			Timestamp: time.Now(),
		})
		return
	}
	// クールダウン中は通知しない。復帰通知も抑止するため reason を空にする。
	e.agentOfflineReason = ""
	e.mu.Unlock()
}

// ============================================================
// OnAgentDisconnect is called when the agent's WebSocket drops.
// ============================================================
func (e *Engine) OnAgentDisconnect() {
	e.mu.Lock()
	if !e.agentOnline {
		e.mu.Unlock()
		return
	}
	e.agentOnline = false
	if e.allowAgentOfflineNotifyLocked(time.Now()) {
		e.agentOfflineReason = "disconnect"
		e.mu.Unlock()

		e.dispatch(&notify.Alert{
			Type:      "agent_disconnect",
			Level:     notify.LevelWarning,
			Icon:      iconForLevel(notify.LevelWarning),
			Title:     "Agent 切断",
			Message:   "Agentが切断されました。",
			Timestamp: time.Now(),
		})
		return
	}
	// クールダウン中は通知しない。復帰通知も抑止するため reason を空にする。
	e.agentOfflineReason = ""
	e.mu.Unlock()
}

// allowAgentOfflineNotifyLocked reports whether an agent-offline notification
// may be sent now. It throttles a flapping connection using the configured
// cooldown so channels are not spammed with 切断/復帰 pairs.
// Caller must hold e.mu.
func (e *Engine) allowAgentOfflineNotifyLocked(now time.Time) bool {
	if e.cfg.Cooldown > 0 && !e.agentOfflineNotified.IsZero() && now.Sub(e.agentOfflineNotified) < e.cfg.Cooldown {
		return false
	}
	e.agentOfflineNotified = now
	return true
}

// ============================================================
// Common helpers
// ============================================================
func levelRank(l notify.Level) int {
	switch l {
	case notify.LevelSuccess:
		return 0
	case notify.LevelInfo:
		return 1
	case notify.LevelWarning:
		return 2
	case notify.LevelCritical:
		return 3
	}
	return 0
}

func shouldFire(st *metricState, hold, cooldown time.Duration, now time.Time, level notify.Level) bool {
	if st.startedAt.IsZero() {
		return false
	}
	if now.Sub(st.startedAt) < hold {
		return false
	}
	if !st.lastNotified.IsZero() && now.Sub(st.lastNotified) < cooldown {
		if levelRank(level) <= levelRank(st.firedLevel) {
			return false
		}
	}
	return true
}

func shouldRecover(st *metricState, recoveryHold time.Duration, now time.Time) bool {
	if st.firedLevel == "" {
		return false
	}
	if st.normalSince.IsZero() {
		return false
	}
	return now.Sub(st.normalSince) >= recoveryHold
}

// iconForLevel returns a representative emoji for a notification level.
// Discord / Slack / Telegram などの通知で使う。
func iconForLevel(level notify.Level) string {
	switch level {
	case notify.LevelCritical:
		return "🚨"
	case notify.LevelWarning:
		return "⚠️"
	case notify.LevelSuccess:
		return "✅"
	default:
		return "ℹ️"
	}
}

// ============================================================
// Memory
// ============================================================
func (e *Engine) evalMemory(pct float64, now time.Time) *notify.Alert {
	isCritical := pct >= e.cfg.MemoryCritical
	isWarn := pct >= e.cfg.MemoryWarn
	cond := isCritical || isWarn

	st := e.getState("memory")

	if cond {
		if st.startedAt.IsZero() {
			st.startedAt = now
		}
		st.normalSince = time.Time{}

		level := notify.LevelWarning
		title := "メモリ警告"
		if isCritical {
			level = notify.LevelCritical
			title = "メモリ異常"
		}

		if !shouldFire(st, e.cfg.HoldDuration, e.cfg.Cooldown, now, level) {
			return nil
		}
		st.lastNotified = now
		st.firedLevel = level

		msg := fmt.Sprintf("メモリ使用率が高まっています！（現在: %.1f%%）至急確認して下さい！", pct)
		if !isCritical {
			msg = fmt.Sprintf("メモリ使用率が%.1f%%に達しました。", pct)
		}
		return &notify.Alert{
			Type: "memory", Level: level, Icon: iconForLevel(level), Title: title,
			Message: msg, Timestamp: now,
		}
	}

	st.startedAt = time.Time{}
	if st.firedLevel == "" {
		return nil
	}
	if st.normalSince.IsZero() {
		st.normalSince = now
		return nil
	}
	if !shouldRecover(st, e.cfg.RecoveryHold, now) {
		return nil
	}
	if !e.cfg.NotifyRecovery {
		st.firedLevel = ""
		st.normalSince = time.Time{}
		return nil
	}

	st.firedLevel = ""
	st.normalSince = time.Time{}
	st.lastNotified = now
	return &notify.Alert{
		Type: "memory_recovery", Level: notify.LevelSuccess, Icon: iconForLevel(notify.LevelSuccess),
		Title:     "メモリ復旧",
		Message:   fmt.Sprintf("メモリ使用率が正常に戻りました。（現在: %.1f%%）", pct),
		Timestamp: now,
	}
}

// ============================================================
// Free storage
// ============================================================
func (e *Engine) evalDiskFree(freePct float64, freeBytes uint64, now time.Time) *notify.Alert {
	isCritical := freePct <= e.cfg.DiskFreeCritical
	isWarn := freePct <= e.cfg.DiskFreeWarn
	cond := isCritical || isWarn

	st := e.getState("disk_free")

	if cond {
		if st.startedAt.IsZero() {
			st.startedAt = now
		}
		st.normalSince = time.Time{}

		level := notify.LevelWarning
		title := "ストレージ警告"
		if isCritical {
			level = notify.LevelCritical
			title = "ストレージ異常"
		}

		if !shouldFire(st, e.cfg.HoldDuration, e.cfg.Cooldown, now, level) {
			return nil
		}
		st.lastNotified = now
		st.firedLevel = level

		var msg string
		if isCritical {
			msg = fmt.Sprintf(
				"ストレージの空き容量が少なくなっています！（残り: %s / %.1f%%）至急確認してください！",
				formatStorage(freeBytes), freePct,
			)
		} else {
			msg = fmt.Sprintf("ストレージの空き容量が%.1f%%になりました。", freePct)
		}
		return &notify.Alert{
			Type: "disk", Level: level, Icon: iconForLevel(level), Title: title,
			Message: msg, Timestamp: now,
		}
	}

	st.startedAt = time.Time{}
	if st.firedLevel == "" {
		return nil
	}
	if st.normalSince.IsZero() {
		st.normalSince = now
		return nil
	}
	if !shouldRecover(st, e.cfg.RecoveryHold, now) {
		return nil
	}
	if !e.cfg.NotifyRecovery {
		st.firedLevel = ""
		st.normalSince = time.Time{}
		return nil
	}

	st.firedLevel = ""
	st.normalSince = time.Time{}
	st.lastNotified = now
	return &notify.Alert{
		Type: "disk_recovery", Level: notify.LevelSuccess, Icon: iconForLevel(notify.LevelSuccess),
		Title: "ストレージ復旧",
		Message: fmt.Sprintf(
			"ストレージの空き容量が回復しました。（現在: 空き %s / %.1f%%）",
			formatStorage(freeBytes), freePct,
		),
		Timestamp: now,
	}
}

// ============================================================
// CPU temperature
// ============================================================
// minValidCPUTemp distinguishes a missing sensor (0) from a real low value.
// Values at or below this are treated as unavailable.
const minValidCPUTemp = 25.0

func (e *Engine) evalCPUTemp(tempC float64, now time.Time) *notify.Alert {
	if tempC <= minValidCPUTemp {
		// The sensor is unavailable (0) or reports an implausible low value.
		// Clear any fired state so a later recovery does not get stuck.
		st := e.getState("cpu_temp")
		st.startedAt = time.Time{}
		st.normalSince = time.Time{}
		st.firedLevel = ""
		return nil
	}

	isCritical := tempC >= e.cfg.CPUTempCritical
	isWarn := tempC >= e.cfg.CPUTempWarn
	cond := isCritical || isWarn

	st := e.getState("cpu_temp")

	if cond {
		if st.startedAt.IsZero() {
			st.startedAt = now
		}
		st.normalSince = time.Time{}

		level := notify.LevelWarning
		title := "CPU温度警告"
		if isCritical {
			level = notify.LevelCritical
			title = "CPU温度異常"
		}

		if !shouldFire(st, e.cfg.HoldDuration, e.cfg.Cooldown, now, level) {
			return nil
		}
		st.lastNotified = now
		st.firedLevel = level

		var msg string
		if isCritical {
			msg = fmt.Sprintf("CPU温度が%.1f°Cになっています！至急確認してください。", tempC)
		} else {
			msg = fmt.Sprintf("CPU温度が%.1f°Cに達しました。", tempC)
		}
		return &notify.Alert{
			Type: "cpu_temp", Level: level, Icon: iconForLevel(level), Title: title,
			Message: msg, Timestamp: now,
		}
	}

	st.startedAt = time.Time{}
	if st.firedLevel == "" {
		return nil
	}
	if st.normalSince.IsZero() {
		st.normalSince = now
		return nil
	}
	if !shouldRecover(st, e.cfg.RecoveryHold, now) {
		return nil
	}
	if !e.cfg.NotifyRecovery {
		st.firedLevel = ""
		st.normalSince = time.Time{}
		return nil
	}

	st.firedLevel = ""
	st.normalSince = time.Time{}
	st.lastNotified = now
	return &notify.Alert{
		Type: "cpu_temp_recovery", Level: notify.LevelSuccess, Icon: iconForLevel(notify.LevelSuccess),
		Title:     "CPU温度復旧",
		Message:   fmt.Sprintf("CPU温度が正常範囲に戻りました。（現在: %.1f°C）", tempC),
		Timestamp: now,
	}
}

// ============================================================
// Disk S.M.A.R.T health
// ============================================================
func (e *Engine) evalDiskHealth(disks []status.DiskInfo, now time.Time) *notify.Alert {
	var badDisks []string
	for _, d := range disks {
		if d.Health == "FAILED" {
			badDisks = append(badDisks, d.Path)
		}
	}

	st := e.getState("disk_health")

	if len(badDisks) > 0 {
		if st.startedAt.IsZero() {
			st.startedAt = now
		}
		st.normalSince = time.Time{}

		if !shouldFire(st, e.cfg.HoldDuration, e.cfg.Cooldown, now, notify.LevelCritical) {
			return nil
		}
		st.lastNotified = now
		st.firedLevel = notify.LevelCritical

		var msg string
		if len(badDisks) == 1 {
			msg = fmt.Sprintf("ディスク（%s）に異常が発生しています。至急確認してください。", badDisks[0])
		} else {
			msg = fmt.Sprintf("複数のディスク（%s）に異常が発生しています。至急確認してください。", strings.Join(badDisks, ", "))
		}
		return &notify.Alert{
			Type: "disk_health", Level: notify.LevelCritical, Icon: "💀",
			Title:   "ディスク異常",
			Message: msg, Timestamp: now,
		}
	}

	st.startedAt = time.Time{}
	if st.firedLevel == "" {
		return nil
	}
	if st.normalSince.IsZero() {
		st.normalSince = now
		return nil
	}
	if !shouldRecover(st, e.cfg.RecoveryHold, now) {
		return nil
	}
	if !e.cfg.NotifyRecovery {
		st.firedLevel = ""
		st.normalSince = time.Time{}
		return nil
	}

	st.firedLevel = ""
	st.normalSince = time.Time{}
	st.lastNotified = now
	return &notify.Alert{
		Type: "disk_health_recovery", Level: notify.LevelSuccess, Icon: iconForLevel(notify.LevelSuccess),
		Title:     "ディスク復旧",
		Message:   "ディスクの異常が解消されました。",
		Timestamp: now,
	}
}

// ============================================================
// Helpers
// ============================================================
func (e *Engine) getState(key string) *metricState {
	st, ok := e.states[key]
	if !ok {
		st = &metricState{}
		e.states[key] = st
	}
	return st
}

func formatStorage(b uint64) string {
	const MB = uint64(1024 * 1024)
	const GB = MB * 1024
	const TB = GB * 1024
	switch {
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(TB))
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	default:
		// A nearly full disk is exactly when the alert matters, so show MB
		// instead of a rounded-to-zero GB value (e.g. "0.4 GB").
		return fmt.Sprintf("%.0f MB", float64(b)/float64(MB))
	}
}
