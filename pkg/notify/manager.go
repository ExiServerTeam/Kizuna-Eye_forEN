package notify

import (
	"context"
	"strings"
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

// ============================================================
// Manager fans out notifications to multiple channels.
// ============================================================
type Manager struct {
	notifiers []Notifier
	log       *logger.Logger
	enabled   bool
	sem       chan struct{}
}

// NewManager creates a Manager.
func NewManager(log *logger.Logger) *Manager {
	return &Manager{
		notifiers: make([]Notifier, 0),
		log:       log,
		enabled:   true,
		sem:       make(chan struct{}, maxConcurrentSends),
	}
}

// FromConfig builds a Manager from a config.NotificationsConfig.
func FromConfig(cfg config.NotificationsConfig, log *logger.Logger) *Manager {
	m := NewManager(log)

	if !cfg.Enabled {
		m.enabled = false
		if log != nil {
			log.Info("通知機能は無効です")
		}
		return m
	}

	for _, ch := range cfg.Channels {
		if !ch.Enabled {
			continue
		}
		// Accept "Discord" / " discord " as well as "discord" so a stray
		// space or capital does not silently drop the channel.
		switch strings.ToLower(strings.TrimSpace(ch.Type)) {
		case "discord":
			m.notifiers = append(m.notifiers, NewDiscordNotifier(ch.WebhookURL))
			if log != nil {
				log.Info("通知チャンネル登録: discord")
			}
		case "telegram":
			m.notifiers = append(m.notifiers, NewTelegramNotifier(ch.BotToken, ch.ChatID))
			if log != nil {
				log.Info("通知チャンネル登録: telegram")
			}
		case "line":
			m.notifiers = append(m.notifiers, NewLINENotifier(ch.Token))
			if log != nil {
				log.Info("通知チャンネル登録: line")
			}
		case "slack":
			m.notifiers = append(m.notifiers, NewSlackNotifier(ch.WebhookURL))
			if log != nil {
				log.Info("通知チャンネル登録: slack")
			}
		case "email":
			m.notifiers = append(m.notifiers, NewEmailNotifier(
				ch.SMTPHost, ch.SMTPPort, ch.SMTPUsername, ch.SMTPPassword, ch.EmailFrom, ch.EmailTo,
			))
			if log != nil {
				log.Info("通知チャンネル登録: email")
			}
		}
	}

	if log != nil {
		log.Info("通知チャンネル数: %d", len(m.notifiers))
	}
	return m
}

// Notify sends the alert to all channels asynchronously.
func (m *Manager) Notify(a *Alert) {
	if a == nil {
		return
	}
	if !m.enabled || len(m.notifiers) == 0 {
		return
	}

	if m.log != nil {
		m.log.Info("通知送信: [%s] %s", a.Level, a.Message)
	}

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
					m.log.Warn("通知の同時実行が上限(%d)のため [%s] への送信をスキップしました", maxConcurrentSends, n.Name())
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
					m.log.Warn("重大通知の送信待ちがタイムアウトしたため [%s] への送信をスキップしました", n.Name())
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
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := notifier.Send(ctx, a); err != nil {
			if m.log != nil {
				m.log.Warn("通知失敗 [%s]: %v", notifier.Name(), err)
			}
			return
		}
		if m.log != nil {
			m.log.Debug("通知成功 [%s]", notifier.Name())
		}
	}()
}

// HasChannels reports whether at least one channel is enabled.
func (m *Manager) HasChannels() bool {
	return m.enabled && len(m.notifiers) > 0
}
