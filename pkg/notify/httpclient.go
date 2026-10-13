package notify

import (
	"fmt"
	"net/http"
	"time"

	"Kizuna-Eye/pkg/config"
)

// newNotifyHTTPClient は通知チャンネル用の *http.Client を返す。
//
// すべてのリクエストを 10 秒のタイムアウトで制限し、決定的に、非公開の宛先への
// リダイレクトを追わない。webhook URL 自体は設定層（config.ValidateWebhookURL）が
// 検証するが、3xx レスポンスは dashboard を内部サービス（loopback、RFC1918、
// クラウドメタデータ）へ向け得る。これはリダイレクト経由の SSRF だ。
// リダイレクト先を同じ規則で再検証することでこの穴を塞ぐ。
//
// pkg/notify はどのプラグインからもインポートされないため、このファイルの変更は
// プラグインのビルドハッシュに影響しない（プラグイン再ビルド不要）。
// newNotifyHTTPClient returns an *http.Client for a notification channel.
//
// It bounds every request with a 10s timeout and, crucially, refuses to follow
// a redirect to a non-public destination. The webhook URL itself is validated
// by the config layer (config.ValidateWebhookURL), but a 3xx response could
// otherwise point the dashboard at an internal service (loopback, RFC1918,
// cloud metadata) — a server-side request forgery via redirect. Re-validating
// the redirect target with the same rules closes that gap.
//
// pkg/notify is not imported by any plugin, so changing this file does not
// affect plugin build hashes (no plugin rebuild required).
func newNotifyHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("リダイレクトが多すぎます（5回で停止）")
			}
			return config.ValidateWebhookURL(req.URL.String())
		},
	}
}
