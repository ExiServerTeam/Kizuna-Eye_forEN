package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Discord 429/5xx リトライの既定値。0 は従来動作（リトライ無し）を保つ。
// Discord 429/5xx retry defaults. A 0 value keeps the old (no retry) behaviour.
const (
	defaultDiscordMaxRetries = 5
	defaultBackoffMaxSec     = 60
)

// ============================================================
// DiscordNotifier は Discord webhook で通知を送る。
// DiscordNotifier sends notifications via a Discord webhook.
// ============================================================
type DiscordNotifier struct {
	webhookURL string
	client     *http.Client
	// maxRetries は 429/5xx レスポンス後のリトライ回数だ。
	// maxRetries is the number of retries after a 429/5xx response.
	maxRetries int
	// backoffMax は指数バックオフの待機上限だ。
	// backoffMax caps the exponential backoff wait.
	backoffMax time.Duration
	// logger はリトライ/結果の行を受け取る（nil 安全）。
	// logger receives retry/result lines (nil-safe).
	logger Logger
}

// Logger は notifier が必要とする最小のログ出力面だ。
// Logger is the minimal logging surface the notifier needs.
type Logger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
	Debug(format string, args ...interface{})
}

// NewDiscordNotifier は既定のリトライ設定で DiscordNotifier を作る。
// NewDiscordNotifier creates a DiscordNotifier with default retry settings.
func NewDiscordNotifier(webhookURL string) *DiscordNotifier {
	return NewDiscordNotifierWithOptions(webhookURL, defaultDiscordMaxRetries, defaultBackoffMaxSec, nil)
}

// NewDiscordNotifierWithOptions は明示的なリトライ設定で DiscordNotifier を作る。
// maxRetries<=0 はリトライを無効化し、backoffMaxSec<=0 は 60 秒を使う。
// NewDiscordNotifierWithOptions creates a DiscordNotifier with explicit retry
// settings. maxRetries<=0 disables retry; backoffMaxSec<=0 uses 60s.
func NewDiscordNotifierWithOptions(webhookURL string, maxRetries, backoffMaxSec int, log Logger) *DiscordNotifier {
	if backoffMaxSec <= 0 {
		backoffMaxSec = defaultBackoffMaxSec
	}
	return &DiscordNotifier{
		webhookURL: webhookURL,
		client:     newNotifyHTTPClient(),
		maxRetries: maxRetries,
		backoffMax: time.Duration(backoffMaxSec) * time.Second,
		logger:     log,
	}
}

func (d *DiscordNotifier) Name() string { return "discord" }

// WithLogger はロガーを付けて notifier を返す（流暢）。
// WithLogger attaches a logger and returns the notifier (fluent).
func (d *DiscordNotifier) WithLogger(log Logger) *DiscordNotifier {
	d.logger = log
	return d
}

func (d *DiscordNotifier) Send(ctx context.Context, a *Alert) error {
	payload := d.buildPayload(a)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("payload marshal: %w", err)
	}

	attempt := 0
	backoff := time.Second
	for {
		attempt++
		retryAfter, retryable, sendErr := d.postOnce(ctx, body)
		if sendErr == nil {
			if attempt > 1 && d.logger != nil {
				d.logger.Info("HTTP 429 リトライ後成功 [discord] (n=%d)", attempt-1)
			}
			return nil
		}

		// リトライ不可（429 以外の 4xx）またはリトライ上限。
		// Not retryable (4xx other than 429) or out of retries.
		if !retryable {
			return sendErr
		}
		if attempt > d.maxRetries {
			if d.logger != nil {
				d.logger.Warn("リトライ上限到達、通知失敗 [discord]: %v", sendErr)
			}
			return sendErr
		}

		// 待機を選ぶ。Retry-After（あれば）が優先、でなければ指数バックオフ。
		// Choose the wait: Retry-After (if present) wins, else exponential backoff.
		wait := backoff
		if retryAfter > 0 {
			wait = retryAfter
		}
		if wait > d.backoffMax {
			wait = d.backoffMax
		}
		if d.logger != nil {
			d.logger.Warn("HTTP 429 受信、%s後に再送 [discord] (%d/%d)", wait.Round(time.Second), attempt, d.maxRetries)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		// 次回リトライ用にバックオフを倍にする（上限付き）。
		// Double the backoff for the next retry, capped.
		backoff *= 2
		if backoff > d.backoffMax {
			backoff = d.backoffMax
		}
	}
}

// buildPayload はアラートを Discord webhook の JSON に変換する。
// buildPayload turns an alert into the Discord webhook JSON.
func (d *DiscordNotifier) buildPayload(a *Alert) map[string]interface{} {
	// Discord は embed の title が 256 文字、description が 4096 文字を超えると
	// 拒否する（HTTP 400）。長いアラート 1 件（例: 多数の故障ディスク）で通知全体が
	// 落ちないよう切り詰める。
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
	return map[string]interface{}{
		"embeds": []interface{}{embed},
		// アラート内容が @everyone / ロール / ユーザーを ping するのを決して許さない。
		// Never allow the alert content to ping @everyone / roles / users.
		"allowed_mentions": map[string]interface{}{
			"parse": []string{},
		},
	}
}

// postOnce はリクエストを 1 回送り、結果を分類する。
//   - retryable は HTTP 429 と 5xx（一時的）で true、その他の 4xx で false。
//   - retryAfter は解析した Retry-After ヘッダ（無し/不正なら 0）。
//
// エラーは最後の戻り値だ（Go の慣習）ので、分類値と取り違えられない。
// postOnce sends the request once and classifies the result:
//   - retryable is true for HTTP 429 and 5xx (transient), false for other 4xx.
//   - retryAfter is the parsed Retry-After header (0 when absent/invalid).
//
// The error is the last result (Go convention) so it is never mistaken for a
// classification value.
func (d *DiscordNotifier) postOnce(ctx context.Context, body []byte) (retryAfter time.Duration, retryable bool, err error) {
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if rerr != nil {
		return 0, false, fmt.Errorf("request build: %w", rerr)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, herr := d.client.Do(req)
	if herr != nil {
		// ネットワークエラー: 一時的として扱う（サーバーが到達不能かもしれない）。
		// Network error: treat as transient (server may be unreachable).
		return 0, true, fmt.Errorf("http post: %w", herr)
	}
	defer resp.Body.Close()
	// 接続を再利用できるよう少しだけ排出する。
	// Drain a little so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return 0, false, nil
	}

	// 429: Retry-After（秒または HTTP-date）を尊重する。
	// 429: honor Retry-After (seconds or HTTP-date).
	if resp.StatusCode == http.StatusTooManyRequests {
		return parseRetryAfter(resp.Header.Get("Retry-After")), true, fmt.Errorf("discord returned status %d", resp.StatusCode)
	}
	// 5xx: 一時的。
	// 5xx: transient.
	if resp.StatusCode >= 500 {
		return 0, true, fmt.Errorf("discord returned status %d", resp.StatusCode)
	}
	// その他の 4xx: 恒久的（不正なペイロード、失効した webhook）。リトライしない。
	// Other 4xx: permanent (bad payload, revoked webhook). Do not retry.
	return 0, false, fmt.Errorf("discord returned status %d", resp.StatusCode)
}

// parseRetryAfter は Retry-After ヘッダ値（秒または HTTP-date）を解析する。
// parseRetryAfter parses a Retry-After header value (seconds or HTTP-date).
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
