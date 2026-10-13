package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ============================================================
// LINENotifier は LINE Notify で通知を送る。
// LINENotifier sends notifications via LINE Notify.
// ============================================================
type LINENotifier struct {
	token  string
	client *http.Client
}

func NewLINENotifier(token string) *LINENotifier {
	return &LINENotifier{
		token:  token,
		client: newNotifyHTTPClient(),
	}
}

func (l *LINENotifier) Name() string { return "line" }

// lineAPIURL はテストで上書き可能にし、実際の LINE Notify API ではなく
// httptest サーバーへ向けられるようにする。
// lineAPIURL is overridable in tests so the notifier can be pointed at an
// httptest server instead of the real LINE Notify API.
var lineAPIURL = "https://notify-api.line.me/api/notify"

func (l *LINENotifier) Send(ctx context.Context, a *Alert) error {
	// LINE Notify は 1000 文字を超えるメッセージを拒否する。
	// 長いアラート 1 件で通知全体が落ちないよう切り詰める。
	// LINE Notify rejects messages longer than 1000 characters.
	// Truncate so one long alert cannot drop the whole notification.
	text := truncateRunes(fmt.Sprintf("\n%s%s\n%s", a.IconPrefix(), a.Title, a.FullMessage()), 1000)

	data := url.Values{}
	data.Set("message", text)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		lineAPIURL,
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
