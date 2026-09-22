package alert

import (
	"fmt"
	"sync"
	"time"

	"Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/notify"
	"Kizuna-Eye/pkg/status"
)

// ============================================================
// Config はアラートエンジンの設定
// ============================================================
type Config struct {
	MemoryWarn       float64
	MemoryCritical   float64
	DiskFreeWarn     float64
	DiskFreeCritical float64
	CPUTempWarn      float64
	CPUTempCritical  float64

	HoldDuration time.Duration // 異常が持続すべき時間
	RecoveryHold time.Duration // 復旧判定に必要な正常継続時間（ヒステリシス）
	Cooldown     time.Duration // 同一アラートの再送禁止時間
	AgentTimeout time.Duration // Agent 応答なしの閾値

	NotifyRecovery bool
}

// metricState は指標ごとの状態
type metricState struct {
	startedAt    time.Time
	normalSince  time.Time
	lastNotified time.Time
	firedLevel   notify.Level
}

// ============================================================
// Engine はアラートを検知して通知する
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
}

func NewEngine(cfg Config, notifier *notify.Manager, log *logger.Logger) *Engine {
	return &Engine{
		cfg:      cfg,
		log:      log,
		notifier: notifier,
		states:   make(map[string]*metricState),
	}
}

// ============================================================
// OnStatus は Agent から新しいデータが届いた時に呼ばれる
// ============================================================
func (e *Engine) OnStatus(s *status.SystemStatus) {
	var pending []*notify.Alert
	now := time.Now()

	e.mu.Lock()

	// ---------- Agent 復帰 ----------
	if !e.agentOnline {
		if e.agentOfflineReason != "" && e.cfg.NotifyRecovery {
			pending = append(pending, &notify.Alert{
				Type:      "agent_recovery",
				Level:     notify.LevelSuccess,
				Icon:      "✅",
				Title:     "Agent 復帰",
				Message:   "Agentからの応答が復帰しました。",
				Timestamp: now,
			})
		}
		e.agentOnline = true
		e.agentOfflineReason = ""
	}
	e.agentLastSeen = now

	// ---------- メモリ ----------
	if a := e.evalMemory(s.MemPercent, now); a != nil {
		pending = append(pending, a)
	}

	// ---------- ストレージ ----------
	freeBytes := uint64(0)
	if s.DiskTotal > s.DiskUsed {
		freeBytes = s.DiskTotal - s.DiskUsed
	}
	diskFreePct := 100.0 - s.DiskPercent
	if a := e.evalDiskFree(diskFreePct, freeBytes, now); a != nil {
		pending = append(pending, a)
	}

	// ---------- CPU温度 ----------
	if a := e.evalCPUTemp(s.CPUTemp, now); a != nil {
		pending = append(pending, a)
	}

	// ---------- ディスクS.M.A.R.T ----------
	if a := e.evalDiskHealth(s.Disks, now); a != nil {
		pending = append(pending, a)
	}

	e.mu.Unlock()

	for _, a := range pending {
		e.notifier.Notify(a)
	}
}

// ============================================================
// CheckAgentTimeout は定期的に呼び出される
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
	e.agentOfflineReason = "timeout"
	e.mu.Unlock()

	e.notifier.Notify(&notify.Alert{
		Type:      "agent_timeout",
		Level:     notify.LevelWarning,
		Icon:      "⚠️",
		Title:     "Agent 応答なし",
		Message:   "Agentから応答がありません。至急確認してください。",
		Timestamp: time.Now(),
	})
}

// ============================================================
// OnAgentDisconnect は Agent の WebSocket が切れた時に呼ばれる
// ============================================================
func (e *Engine) OnAgentDisconnect() {
	e.mu.Lock()
	if !e.agentOnline {
		e.mu.Unlock()
		return
	}
	e.agentOnline = false
	e.agentOfflineReason = "disconnect"
	e.mu.Unlock()

	e.notifier.Notify(&notify.Alert{
		Type:      "agent_disconnect",
		Level:     notify.LevelWarning,
		Icon:      "⚠️",
		Title:     "Agent 切断",
		Message:   "Agentが切断されました。",
		Timestamp: time.Now(),
	})
}

// ============================================================
// 共通ヘルパー
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

// ============================================================
// メモリ
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
		icon := "⚠️"
		title := "メモリ警告"
		if isCritical {
			level = notify.LevelCritical
			icon = "🚨"
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
			Type: "memory", Level: level, Icon: icon, Title: title,
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
		Type: "memory_recovery", Level: notify.LevelSuccess,
		Icon: "✅", Title: "メモリ復旧",
		Message:   fmt.Sprintf("メモリ使用率が正常に戻りました。（現在: %.1f%%）", pct),
		Timestamp: now,
	}
}

// ============================================================
// ストレージ空き
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
		icon := "⚠️"
		title := "ストレージ警告"
		if isCritical {
			level = notify.LevelCritical
			icon = "🚨"
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
			Type: "disk", Level: level, Icon: icon, Title: title,
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
		Type: "disk_recovery", Level: notify.LevelSuccess,
		Icon: "✅", Title: "ストレージ復旧",
		Message: fmt.Sprintf(
			"ストレージの空き容量が回復しました。（現在: 空き %s / %.1f%%）",
			formatStorage(freeBytes), freePct,
		),
		Timestamp: now,
	}
}

// ============================================================
// CPU温度
// ============================================================
func (e *Engine) evalCPUTemp(tempC float64, now time.Time) *notify.Alert {
	if tempC <= 25 {
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
		icon := "⚠️"
		title := "CPU温度警告"
		if isCritical {
			level = notify.LevelCritical
			icon = "🌡️"
			title = "CPU温度異常"
		}

		if !shouldFire(st, e.cfg.HoldDuration, e.cfg.Cooldown, now, level) {
			return nil
		}
		st.lastNotified = now
		st.firedLevel = level

		var msg string
		if isCritical {
			msg = fmt.Sprintf("CPU温度が%.1f度になっています！（現在: %.1f°C）至急確認してください。", tempC, tempC)
		} else {
			msg = fmt.Sprintf("CPU温度が%.1f度に達しました。（現在: %.1f°C）", tempC, tempC)
		}
		return &notify.Alert{
			Type: "cpu_temp", Level: level, Icon: icon, Title: title,
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
		Type: "cpu_temp_recovery", Level: notify.LevelSuccess,
		Icon: "✅", Title: "CPU温度復旧",
		Message:   fmt.Sprintf("CPU温度が正常範囲に戻りました。（現在: %.1f°C）", tempC),
		Timestamp: now,
	}
}

// ============================================================
// ディスクS.M.A.R.T 健康状態
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
			msg = fmt.Sprintf("複数のディスク（%s）に異常が発生しています。至急確認してください。", joinStrings(badDisks, ", "))
		}
		return &notify.Alert{
			Type: "disk_health", Level: notify.LevelCritical,
			Icon: "💀", Title: "ディスク異常",
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
		Type: "disk_health_recovery", Level: notify.LevelSuccess,
		Icon: "✅", Title: "ディスク復旧",
		Message:   "ディスクの異常が解消されました。",
		Timestamp: now,
	}
}

// ============================================================
// ヘルパー
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
	const GB = uint64(1024 * 1024 * 1024)
	const TB = GB * 1024
	if b >= TB {
		return fmt.Sprintf("%.2f TB", float64(b)/float64(TB))
	}
	return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
}

func joinStrings(list []string, sep string) string {
	if len(list) == 0 {
		return ""
	}
	out := list[0]
	for _, s := range list[1:] {
		out += sep + s
	}
	return out
}
