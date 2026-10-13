package config

import "testing"

// TestIsPlaceholderWebhookURLDiscordFake: an unedited Discord webhook that uses
// an obviously-fake id/token (0000.../xxxx...) contains none of the text
// markers, so it must still be detected as a placeholder. Otherwise the
// channel registers a notifier and fails every send with HTTP 400 forever —
// a silent notification outage (設計原則「無言の失敗を許さない」).
func TestIsPlaceholderWebhookURLDiscordFake(t *testing.T) {
	fakes := []string{
		"https://discord.com/api/webhooks/000000000000000000/xxxxxxxxxxxxxxxxxxxxxxxx",
		"https://discord.com/api/webhooks/000000000000000000/0000000000000000000000",
		"https://canary.discord.com/api/webhooks/111111111111111111/aaaaAAAAAAAAaaaaaaaa",
	}
	for _, u := range fakes {
		if !IsPlaceholderWebhookURL(u) {
			t.Errorf("IsPlaceholderWebhookURL(%q) = false, want true", u)
		}
		if err := ValidateWebhookURL(u); err == nil {
			t.Errorf("ValidateWebhookURL(%q) = nil, want placeholder error", u)
		}
	}
}

// TestIsPlaceholderWebhookURLRealNotFlagged: a plausible real Discord webhook
// (mixed id/token, all different characters) must NOT be treated as a
// placeholder, so a real deployment keeps notifying.
func TestIsPlaceholderWebhookURLRealNotFlagged(t *testing.T) {
	real := []string{
		"https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
		"https://hooks.slack.com/services/T00000000/B00000000/abcdefghijklmnop",
	}
	for _, u := range real {
		if IsPlaceholderWebhookURL(u) {
			t.Errorf("IsPlaceholderWebhookURL(%q) = true, want false", u)
		}
	}
}

// TestIsRepeatedChar: the helper only flags a single repeated character of
// sufficient length.
func TestIsRepeatedChar(t *testing.T) {
	if !isRepeatedChar("xxxxxxxxxxxx", 12) {
		t.Error("all-x 12 chars should be repeated")
	}
	if isRepeatedChar("xxxxxxxxxxx", 12) {
		t.Error("11 chars should be below the threshold")
	}
	if isRepeatedChar("xxyxxxxxxxxx", 12) {
		t.Error("a mixed string is not repeated")
	}
	if isRepeatedChar("", 12) {
		t.Error("empty string is not repeated")
	}
}
