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
// SlackNotifier sends notifications via a Slack incoming webhook.
// ============================================================
type SlackNotifier struct {
	webhookURL string
	client     *http.Client
}

func NewSlackNotifier(webhookURL string) *SlackNotifier {
	return &SlackNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *SlackNotifier) Name() string { return "slack" }

func (s *SlackNotifier) Send(ctx context.Context, a *Alert) error {
	// Combine icon, title, and body into the Slack text field. Slack text
	// blocks are limited to 3000 characters; truncate so a long alert is not
	// rejected wholesale. Title/body may contain attacker-controlled data, so
	// escape mrkdwn before embedding it (prevents mention/link injection).
	alertTitle := escapeSlackMrkdwn(a.Title)
	alertBody := escapeSlackMrkdwn(a.FullMessage())
	text := truncateRunes(fmt.Sprintf("%s*%s*\n%s", a.IconPrefix(), alertTitle, alertBody), 3000)

	payload := map[string]interface{}{
		"text": text,
		// Disallow link previews and any mention parsing triggered by the
		// alert content.
		"unfurl_links": false,
		"unfurl_media": false,
		"attachments": []map[string]interface{}{
			{
				"color":     fmt.Sprintf("#%06X", a.Color()),
				"footer":    "Kizuna-Eye Notification",
				"ts":        a.Timestamp.Unix(),
				"mrkdwn_in": []string{},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("payload marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack returned status %d", resp.StatusCode)
	}
	return nil
}
