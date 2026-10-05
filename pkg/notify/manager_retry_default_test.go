package notify

import (
	"testing"
	"time"

	"Kizuna-Eye/pkg/config"
)

// discordCfg returns a minimal config with one enabled Discord channel.
func discordCfg() config.NotificationsConfig {
	return config.NotificationsConfig{
		Enabled: true,
		Channels: []config.NotificationChannel{
			{Type: "discord", Enabled: true, WebhookURL: "https://discord.example.invalid/api/webhooks/1/x"},
		},
	}
}

// discordFromConfig builds the Manager and returns its Discord notifier.
func discordFromConfig(t *testing.T, cfg config.NotificationsConfig) *DiscordNotifier {
	t.Helper()
	m := FromConfig(cfg, nil)
	for _, n := range m.notifiers {
		if d, ok := n.(*DiscordNotifier); ok {
			return d
		}
	}
	t.Fatal("discord notifier が登録されていません")
	return nil
}

// TestDiscordRetryDefaultsWhenKeyAbsent is the regression test for the attack
// test finding: dashboard_config.json never set the retry keys, so a plain int
// field read Go's zero value (0 = "retry disabled") even though the documented
// default is 5. Every 429 was then dropped without a retry.
func TestDiscordRetryDefaultsWhenKeyAbsent(t *testing.T) {
	d := discordFromConfig(t, discordCfg())
	if d.maxRetries != defaultDiscordMaxRetries {
		t.Fatalf("maxRetries = %d, want %d (default when the key is absent)", d.maxRetries, defaultDiscordMaxRetries)
	}
	if d.backoffMax != time.Duration(defaultBackoffMaxSec)*time.Second {
		t.Fatalf("backoffMax = %s, want %ds", d.backoffMax, defaultBackoffMaxSec)
	}
}

// TestDiscordRetryExplicitValues pins the explicit overrides: 0 keeps the
// legacy "no retry" behaviour and a positive value is honored.
func TestDiscordRetryExplicitValues(t *testing.T) {
	zero := 0
	cfg := discordCfg()
	cfg.DiscordMaxRetries = &zero
	if d := discordFromConfig(t, cfg); d.maxRetries != 0 {
		t.Fatalf("maxRetries = %d, want 0 (explicitly disabled)", d.maxRetries)
	}

	three := 3
	cfg = discordCfg()
	cfg.DiscordMaxRetries = &three
	cfg.DiscordBackoffMaxSec = 10
	d := discordFromConfig(t, cfg)
	if d.maxRetries != 3 || d.backoffMax != 10*time.Second {
		t.Fatalf("got retries=%d backoff=%s, want 3 / 10s", d.maxRetries, d.backoffMax)
	}
}

// TestSendTimeoutCoversRetryBudget guards the second half of the same bug: the
// per-send context must outlive the backoff waits, otherwise the retry loop is
// cancelled by the timeout (and the alert is lost even though retrying would
// have succeeded).
func TestSendTimeoutCoversRetryBudget(t *testing.T) {
	if m := FromConfig(discordCfg(), nil); m.sendTimeout <= legacySendTimeout {
		t.Fatalf("sendTimeout = %s, want more than the legacy %s with retries enabled", m.sendTimeout, legacySendTimeout)
	}

	zero := 0
	cfg := discordCfg()
	cfg.DiscordMaxRetries = &zero
	if m := FromConfig(cfg, nil); m.sendTimeout != legacySendTimeout {
		t.Fatalf("sendTimeout = %s, want %s when retries are disabled", m.sendTimeout, legacySendTimeout)
	}
}

// TestSendTimeoutFor pins the budget arithmetic (15s + 1+2+4+... capped).
func TestSendTimeoutFor(t *testing.T) {
	cases := []struct {
		retries int
		capSec  int
		want    time.Duration
	}{
		{0, 60, legacySendTimeout},
		{1, 60, legacySendTimeout + 1*time.Second},
		{2, 60, legacySendTimeout + 3*time.Second},
		{5, 60, legacySendTimeout + 31*time.Second}, // 1+2+4+8+16
		{5, 3, legacySendTimeout + 12*time.Second},  // 1+2+3+3+3
		{100, 60, maxSendTimeout},                   // capped at 2m
	}
	for _, c := range cases {
		if got := sendTimeoutFor(c.retries, time.Duration(c.capSec)*time.Second); got != c.want {
			t.Errorf("sendTimeoutFor(%d, %ds) = %s, want %s", c.retries, c.capSec, got, c.want)
		}
	}
}
