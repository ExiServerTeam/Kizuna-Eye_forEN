package notify

import (
	"fmt"
	"net/http"
	"time"

	"Kizuna-Eye/pkg/config"
)

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
