package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/logger"
)

// maxConcurrentSends bounds the number of in-flight notification goroutines
// so an alert storm cannot spawn an unbounded number of them.
const maxConcurrentSends = 32

// criticalSendWait bounds how long a critical alert waits for a free send slot
// when the manager is saturated. Non-critical alerts are dropped instead, so a
// burst of low-priority noise can never starve a critical notification.
const criticalSendWait = 5 * time.Second

// batchMaxItems bounds one batched message so a flood cannot build an
// enormous Discord embed (which would be rejected anyway).
const batchMaxItems = 50

// legacySendTimeout keeps the original 15s budget for a send when retries are
// disabled, so an installation that never sets the retry keys behaves exactly
// as before.
const legacySendTimeout = 15 * time.Second

// maxSendTimeout caps the per-send budget even with a large retry/backoff
// configuration, so one unreachable webhook cannot hold a send slot forever.
const maxSendTimeout = 2 * time.Minute

// sendTimeoutFor returns how long a single Send may take: legacySendTimeout
// plus the worst-case sum of the exponential backoff waits (1s, 2s, 4s, ...
// capped at backoffMax). Without this, the 15s context cancels the retry loop
// mid-backoff and the alert is lost even though retrying would have worked.
func sendTimeoutFor(retries int, backoffMax time.Duration) time.Duration {
	if retries <= 0 {
		return legacySendTimeout
	}
	total := legacySendTimeout
	wait := time.Second
	for i := 0; i < retries; i++ {
		if wait > backoffMax {
			wait = backoffMax
		}
		total += wait
		wait *= 2
	}
	if total > maxSendTimeout {
		total = maxSendTimeout
	}
	return total
}

// ============================================================
// Manager fans out notifications to multiple channels.
// ============================================================
type Manager struct {
	notifiers []Notifier
	log       *logger.Logger
	enabled   bool
	sem       chan struct{}

	// --- バッチ集約 (タスク1) ---
	// batchEnabled groups alerts that arrive within batchWindow into one
	// message. History is recorded per-alert by the caller; only the
	// notification is aggregated.
	batchEnabled         bool
	batchWindow          time.Duration
	batchExcludeCritical bool

	batchMu    sync.Mutex
	batch      []*Alert
	batchTimer *time.Timer

	// sendTimeout is the per-send context budget (see sendTimeoutFor).
	sendTimeout time.Duration
}

// NewManager creates a Manager.
func NewManager(log *logger.Logger) *Manager {
	return &Manager{
		notifiers:   make([]Notifier, 0),
		log:         log,
		enabled:     true,
		sem:         make(chan struct{}, maxConcurrentSends),
		sendTimeout: legacySendTimeout,
	}
}

// FromConfig builds a Manager from a config.NotificationsConfig.
func FromConfig(cfg config.NotificationsConfig, log *logger.Logger) *Manager {
	m := NewManager(log)

	// Batch settings (defaults match the plan: enabled, 5s window, exclude critical).
	// *bool: absent key means the default (true).
	m.batchEnabled = cfg.BatchEnabled == nil || *cfg.BatchEnabled
	m.batchWindow = time.Duration(cfg.BatchWindowSec) * time.Second
	if m.batchWindow <= 0 {
		m.batchWindow = 5 * time.Second
	}
	// *bool: absent key means default true.
	m.batchExcludeCritical = cfg.BatchExcludeCritical == nil || *cfg.BatchExcludeCritical

	if !cfg.Enabled {
		m.enabled = false
		if log != nil {
			log.InfoT("notify.disabled")
		}
		return m
	}

	// *int: an absent key means the default (5). Reading it as a plain int
	// turned "key not set" into 0, which silently disabled every retry.
	retries := defaultDiscordMaxRetries
	if cfg.DiscordMaxRetries != nil {
		retries = *cfg.DiscordMaxRetries
		if retries < 0 {
			retries = 0
		}
	}
	backoffMax := cfg.DiscordBackoffMaxSec
	if backoffMax <= 0 {
		backoffMax = defaultBackoffMaxSec
	}
	// Keep the per-send budget in step with the retry settings, so the
	// context does not cancel the backoff it was configured to perform.
	m.sendTimeout = sendTimeoutFor(retries, time.Duration(backoffMax)*time.Second)

	for _, ch := range cfg.Channels {
		if !ch.Enabled {
			continue
		}
		// Accept "Discord" / " discord " as well as "discord" so a stray
		// space or capital does not silently drop the channel.
		switch strings.ToLower(strings.TrimSpace(ch.Type)) {
		case "discord":
			m.notifiers = append(m.notifiers, NewDiscordNotifierWithOptions(ch.WebhookURL, retries, backoffMax, loggerAdapter{log}))
			if log != nil {
				log.InfoT("notify.discord_reg", retries, backoffMax)
			}
		case "telegram":
			m.notifiers = append(m.notifiers, NewTelegramNotifier(ch.BotToken, ch.ChatID))
			if log != nil {
				log.InfoT("notify.telegram_reg")
			}
		case "line":
			m.notifiers = append(m.notifiers, NewLINENotifier(ch.Token))
			if log != nil {
				log.InfoT("notify.line_reg")
			}
		case "slack":
			m.notifiers = append(m.notifiers, NewSlackNotifier(ch.WebhookURL))
			if log != nil {
				log.InfoT("notify.slack_reg")
			}
		case "email":
			m.notifiers = append(m.notifiers, NewEmailNotifier(
				ch.SMTPHost, ch.SMTPPort, ch.SMTPUsername, ch.SMTPPassword, ch.EmailFrom, ch.EmailTo,
			))
			if log != nil {
				log.InfoT("notify.email_reg")
			}
		}
	}

	if log != nil {
		log.InfoT("notify.channel_count", len(m.notifiers))
		if m.batchEnabled {
			log.InfoT("notify.batch_enabled", m.batchWindow, m.batchExcludeCritical)
		}
	}
	return m
}

// loggerAdapter bridges *logger.Logger to the notify.Logger interface.
type loggerAdapter struct{ log *logger.Logger }

func (a loggerAdapter) Info(f string, args ...interface{}) {
	if a.log != nil {
		a.log.Info(f, args...)
	}
}
func (a loggerAdapter) Warn(f string, args ...interface{}) {
	if a.log != nil {
		a.log.Warn(f, args...)
	}
}
func (a loggerAdapter) Error(f string, args ...interface{}) {
	if a.log != nil {
		a.log.Error(f, args...)
	}
}
func (a loggerAdapter) Debug(f string, args ...interface{}) {
	if a.log != nil {
		a.log.Debug(f, args...)
	}
}

// Notify records the alert and dispatches it to all channels. When batching
// is enabled, non-critical alerts are held for batchWindow and sent as one
// message; critical alerts (when batchExcludeCritical is set) go out at once.
func (m *Manager) Notify(a *Alert) {
	if a == nil {
		return
	}
	if !m.enabled || len(m.notifiers) == 0 {
		return
	}

	if m.log != nil {
		m.log.InfoT("notify.send", a.Level, a.Message)
	}

	// Batching path: hold non-critical (and critical, if not excluded) alerts.
	if m.batchEnabled {
		isCritical := a.Level == LevelCritical
		if !(isCritical && m.batchExcludeCritical) {
			m.addToBatch(a)
			return
		}
	}

	m.dispatch(a)
}

// addToBatch appends an alert to the current window and schedules the flush.
func (m *Manager) addToBatch(a *Alert) {
	m.batchMu.Lock()
	m.batch = append(m.batch, a)
	if m.batchTimer == nil {
		m.batchTimer = time.AfterFunc(m.batchWindow, m.flushBatch)
	}
	m.batchMu.Unlock()
}

// flushBatch sends the accumulated alerts as one message and resets the window.
func (m *Manager) flushBatch() {
	m.batchMu.Lock()
	items := m.batch
	m.batch = nil
	m.batchTimer = nil
	m.batchMu.Unlock()

	if len(items) == 0 {
		return
	}
	if len(items) == 1 {
		m.dispatch(items[0])
		return
	}
	m.dispatch(m.buildBatchAlert(items))
}

// buildBatchAlert collapses N alerts into one summary alert. The individual
// alerts remain in the history (the caller records each one); only the
// notification is aggregated.
func (m *Manager) buildBatchAlert(items []*Alert) *Alert {
	n := len(items)
	shown := n
	if shown > batchMaxItems {
		shown = batchMaxItems
	}

	// Highest level wins so the batch is not understated.
	level := LevelInfo
	for _, a := range items {
		if levelWeight(a.Level) > levelWeight(level) {
			level = a.Level
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d 件のアラート（%s 間に集約）\n", n, m.batchWindow)
	for i := 0; i < shown; i++ {
		a := items[i]
		fmt.Fprintf(&b, "• [%s] %s\n", a.Level, a.FullTitle())
	}
	if n > shown {
		fmt.Fprintf(&b, "… 他 %d 件（履歴には個別に記録されています）\n", n-shown)
	}

	// Timestamp = newest item so the batch appears at the latest moment.
	ts := items[0].Timestamp
	for _, a := range items {
		if a.Timestamp.After(ts) {
			ts = a.Timestamp
		}
	}

	return &Alert{
		Type:      "batch",
		Level:     level,
		Icon:      items[0].Icon,
		Title:     fmt.Sprintf("アラート集約 (%d件)", n),
		Message:   b.String(),
		Timestamp: ts,
	}
}

// levelWeight ranks levels so the batch reports the highest one.
func levelWeight(l Level) int {
	switch l {
	case LevelCritical:
		return 3
	case LevelWarning:
		return 2
	default:
		return 1
	}
}

// dispatch sends one alert to every notifier, honoring the concurrency cap.
func (m *Manager) dispatch(a *Alert) {
	isCritical := a.Level == LevelCritical
	for _, n := range m.notifiers {
		select {
		case m.sem <- struct{}{}:
			m.sendAsync(n, a)
		default:
			// Saturated. A critical alert is worth waiting for a slot (bounded),
			// so it is not lost. Non-critical alerts are dropped to protect the
			// system from an alert storm.
			if !isCritical {
				if m.log != nil {
					m.log.WarnT("notify.concurrent_skip", maxConcurrentSends, n.Name())
				}
				continue
			}
			timer := time.NewTimer(criticalSendWait)
			select {
			case m.sem <- struct{}{}:
				timer.Stop()
				m.sendAsync(n, a)
			case <-timer.C:
				if m.log != nil {
					m.log.WarnT("notify.timeout_skip", n.Name())
				}
			}
		}
	}
}

// sendAsync runs one notifier in its own goroutine, holding a send slot until
// it finishes.
func (m *Manager) sendAsync(notifier Notifier, a *Alert) {
	go func() {
		defer func() { <-m.sem }()
		// Recover so a panic inside a notifier's Send cannot crash the whole
		// process (which would stop all monitoring). The slot is still
		// released by the deferred receive above.
		defer func() {
			if r := recover(); r != nil {
				if m.log != nil {
					m.log.ErrorT("notify.panic", notifier.Name(), r)
				}
			}
		}()
		timeout := m.sendTimeout
		if timeout <= 0 {
			timeout = legacySendTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := notifier.Send(ctx, a); err != nil {
			if m.log != nil {
				m.log.WarnT("notify.failed", notifier.Name(), err)
			}
			return
		}
		if m.log != nil {
			m.log.DebugT("notify.success", notifier.Name())
		}
	}()
}

// HasChannels reports whether at least one channel is enabled.
func (m *Manager) HasChannels() bool {
	return m.enabled && len(m.notifiers) > 0
}

// Flush sends any pending batched alerts immediately. Call on shutdown so a
// pending window is not lost.
func (m *Manager) Flush() {
	m.batchMu.Lock()
	if m.batchTimer != nil {
		m.batchTimer.Stop()
	}
	m.batchMu.Unlock()
	m.flushBatch()
}
