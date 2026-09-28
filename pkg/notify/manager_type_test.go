package notify

import (
	"testing"

	"Kizuna-Eye/pkg/config"
)

// A stray capital or surrounding whitespace in the channel type must not
// silently drop the channel; it should register like the canonical form.
func TestFromConfigNormalizesChannelType(t *testing.T) {
	cfg := config.NotificationsConfig{
		Enabled: true,
		Channels: []config.NotificationChannel{
			{Type: "Discord", Enabled: true, WebhookURL: "https://example.com/hook"},
			{Type: "  telegram ", Enabled: true, BotToken: "t", ChatID: "c"},
		},
	}
	m := FromConfig(cfg, nil)
	if !m.HasChannels() {
		t.Fatal("expected normalized channels to be registered")
	}
	if len(m.notifiers) != 2 {
		t.Fatalf("notifiers = %d, want 2", len(m.notifiers))
	}
}
