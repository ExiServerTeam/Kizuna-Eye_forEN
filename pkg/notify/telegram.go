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
// TelegramNotifier は Telegram Bot API で通知を送る
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
	text := fmt.Sprintf("%s *%s*\n%s", a.Icon, a.Title, a.FullMessage())

	payload := map[string]string{
		"chat_id":    t.chatID,
		"text":       text,
		"parse_mode": "Markdown",
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
