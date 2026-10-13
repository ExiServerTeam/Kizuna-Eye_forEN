package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"time"
)

// ============================================================
// TelegramNotifier sends notifications via the Telegram Bot API.
// ============================================================
type TelegramNotifier struct {
	botToken string
	chatID   string
	client   *http.Client
}

func NewTelegramNotifier(botToken, chatID string) *TelegramNotifier {
	return &TelegramNotifier{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *TelegramNotifier) Name() string { return "telegram" }

func (t *TelegramNotifier) Send(ctx context.Context, a *Alert) error {
	// Use HTML mode and escape dynamic values to avoid 400s from special characters.
	title := html.EscapeString(a.Title)
	msg := html.EscapeString(a.FullMessage())
	// Telegram rejects messages longer than 4096 characters (HTTP 400).
	// Truncate so one long alert cannot drop the whole notification.
	text := truncateRunes(fmt.Sprintf("%s<b>%s</b>\n%s", a.IconPrefix(), title, msg), 4096)

	payload := map[string]string{
		"chat_id":    t.chatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("payload marshal: %w", err)
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram returned status %d", resp.StatusCode)
	}
	return nil
}
