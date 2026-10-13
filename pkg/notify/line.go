package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ============================================================
// LINENotifier は LINE Notify で通知を送る
// ============================================================
type LINENotifier struct {
	token  string
	client *http.Client
}

func NewLINENotifier(token string) *LINENotifier {
	return &LINENotifier{
		token:  token,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (l *LINENotifier) Name() string { return "line" }

func (l *LINENotifier) Send(ctx context.Context, a *Alert) error {
	text := fmt.Sprintf("\n%s %s\n%s", a.Icon, a.Title, a.FullMessage())

	data := url.Values{}
	data.Set("message", text)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://notify-api.line.me/api/notify",
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return fmt.Errorf("request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+l.token)

	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("line returned status %d", resp.StatusCode)
	}
	return nil
}
