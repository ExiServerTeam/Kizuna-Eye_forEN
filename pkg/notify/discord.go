package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ============================================================
// DiscordNotifier sends notifications via a Discord webhook.
// ============================================================
type DiscordNotifier struct {
	webhookURL string
	client     *http.Client
}

func NewDiscordNotifier(webhookURL string) *DiscordNotifier {
	return &DiscordNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *DiscordNotifier) Name() string { return "discord" }

func (d *DiscordNotifier) Send(ctx context.Context, a *Alert) error {
	// Discord rejects embeds whose title exceeds 256 chars or whose
	// description exceeds 4096 chars (HTTP 400). Truncate so a single long
	// alert (e.g. many failing disks) cannot drop the whole notification.
	embed := map[string]interface{}{
		"title":       truncateRunes(a.FullTitle(), 256),
		"description": truncateRunes(a.FullMessage(), 4096),
		"color":       a.Color(),
		"timestamp":   a.Timestamp.Format(time.RFC3339),
		"footer": map[string]string{
			"text": "Kizuna-Eye Notification",
		},
	}

	payload := map[string]interface{}{
		"embeds": []interface{}{embed},
		// Never allow the alert content to ping @everyone / roles / users.
		"allowed_mentions": map[string]interface{}{
			"parse": []string{},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("payload marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord returned status %d", resp.StatusCode)
	}
	return nil
}
