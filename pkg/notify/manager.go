package notify

import (
	"context"
	"time"

	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/logger"
)

// ============================================================
// Manager は複数チャンネルへの通知を束ねる
// ============================================================
type Manager struct {
	notifiers []Notifier
	log       *logger.Logger
	enabled   bool
}

// NewManager は Manager を生成する
func NewManager(log *logger.Logger) *Manager {
	return &Manager{
		notifiers: make([]Notifier, 0),
		log:       log,
		enabled:   true,
	}
}

// FromConfig は config.NotificationsConfig から Manager を構築する
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
		switch ch.Type {
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
		}
	}

	if log != nil {
		log.Info("通知チャンネル数: %d", len(m.notifiers))
	}
	return m
}

// Notify は全チャンネルに非同期で通知を送る
func (m *Manager) Notify(a *Alert) {
	if !m.enabled || len(m.notifiers) == 0 {
		return
	}

	if m.log != nil {
		m.log.Info("通知送信: [%s] %s", a.Level, a.Message)
	}

	for _, n := range m.notifiers {
		go func(notifier Notifier) {
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
		}(n)
	}
}

// HasChannels は有効なチャンネルが1つ以上あるかを返す
func (m *Manager) HasChannels() bool {
	return m.enabled && len(m.notifiers) > 0
}
