package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateWebhookURL は、dashboard が意図しない宛先へリクエストを出す
// （SSRF）webhook URL を拒否する。URL は管理者が設定し、サーバー側で取得
// されるため、タイポや悪意ある設定が内部サービス（loopback、RFC1918、
// リンクローカル / クラウドメタデータ）へ到達し得る。
//
// 規則:
//   - scheme は http か https でなければならない
//   - ホストが存在し、private / loopback / リンクローカル / unspecified の
//     いずれのアドレスにも解決されてはならない（169.254.169.254 メタデータを含む）
//   - 生の IP リテラルは直接検査する。ホスト名は解決先の全アドレスを検査するため、
//     127.0.0.1 を指す名前も拒否される
//
// 実運用の webhook（discord.com、hooks.slack.com）には公開ホストが必要だ。
// ValidateWebhookURL rejects webhook URLs that would make the dashboard issue
// a request to an unintended destination (SSRF). The URL is set by an admin
// and is fetched server-side, so a typo or a malicious config could otherwise
// reach internal services (loopback, RFC1918, link-local / cloud metadata).
//
// Rules:
//   - scheme must be http or https
//   - the host must be present and must not resolve to a private, loopback,
//     link-local, or unspecified address (this covers 169.254.169.254 metadata)
//   - a bare IP literal is checked directly; a hostname is checked via every
//     address it resolves to, so a name pointing at 127.0.0.1 is rejected too
//
// A public host is required for real webhooks (discord.com, hooks.slack.com).
func ValidateWebhookURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("webhook_url が空です")
	}
	// 未編集のプレースホルダを拒否する。同梱の example 設定は YOUR_WEBHOOK_ID /
	// YOUR_WEBHOOK_TOKEN を含む Discord URL を出荷する。これは有効な URL として
	// 解析され、以下の scheme/host 検査を通過し、送信時に Discord 側で初めて
	// 拒否される（HTTP 400）。その結果 dashboard は通知を一切届けず、リトライ
	// ストームだけを記録する。ここで失敗させれば、保存/読み込み時に明確な
	// 検証エラーになる。
	// Reject an unedited placeholder. The example config ships a Discord URL
	// containing YOUR_WEBHOOK_ID / YOUR_WEBHOOK_TOKEN; it parses as a valid
	// URL, passes the scheme/host checks below, and is only refused by Discord
	// at send time (HTTP 400). The dashboard then silently never notifies the
	// operator while logging a retry storm. Failing here turns that into a
	// clear validation error at save/load.
	if IsPlaceholderWebhookURL(s) {
		return fmt.Errorf("webhook_url がサンプルのままです（YOUR_... を実際の値に置き換えてください）")
	}
	if err := validateWebhookURLShape(s); err != nil {
		return err
	}
	return nil
}

// IsPlaceholderWebhookURL は、URL が明らかなテンプレートマーカーを含み、
// 運用者が編集していない可能性を返す。
//
// これはエクスポートする。通知マネージャが実行時にそのようなチャンネルを
// スキップでき（プレースホルダを永遠にリトライしない）、設定ローダーが
// 起動を拒否せず警告できるようにするためだ。
// IsPlaceholderWebhookURL reports whether a URL still contains an obvious
// template marker that means the operator never edited it.
//
// It is exported so the notification manager can skip such a channel at
// runtime (instead of retrying a placeholder forever) and the config loaders
// can warn instead of refusing to start.
func IsPlaceholderWebhookURL(s string) bool {
	u := strings.ToUpper(s)
	// このプロジェクトが同梱するテンプレート固有のマーカーだけに留める。
	// 広範なトークン（"XXX"、"EXAMPLE.COM"）は意図的に一致させない。
	// example.com はテストや自己ホスト配備で正当なホストであり、
	// これを弾くとホスト名に含むだけの本物の webhook まで拒否してしまう。
	// Keep the markers specific to the templates this project ships. Broad
	// tokens ("XXX", "EXAMPLE.COM") are deliberately NOT matched: example.com
	// is a legitimate host for tests and self-hosted setups, and flagging it
	// would reject a real webhook whose host merely contains it.
	for _, marker := range []string{
		"YOUR_WEBHOOK",       // discord example (ID / TOKEN)
		"YOUR/SLACK/WEBHOOK", // slack example
		"CHANGE_ME",          // agent_token / generic template
		"REPLACE_ME",         // generic template
		"PLACEHOLDER",        // generic template
	} {
		if strings.Contains(u, marker) {
			return true
		}
	}
	// 構造検査: 未編集の Discord webhook は、上記マーカーを含まない明らかな偽
	// id/token（000000000000000000 / xxxxxxxxxxxxxxxxxxxxxxxx）を使うことがある。
	// それだと検証を通過し、notifier を登録し、以後すべての送信が HTTP 400 で
	// 失敗し続ける — 無言の通知障害だ（運用者には「discord enabled」に見えるが
	// 何も届かない）。
	// Structural check: an unedited Discord webhook may use an obviously fake
	// id/token (000000000000000000 / xxxxxxxxxxxxxxxxxxxxxxxx) that contains
	// none of the markers above, so it would validate, register a notifier, and
	// then fail every send with HTTP 400 forever — a silent notification
	// outage (the operator sees "discord enabled" but nothing is delivered).
	if isFakeDiscordWebhook(s) {
		return true
	}
	return false
}

// isFakeDiscordWebhook は、Discord webhook URL が明らかな偽の id または
// token セグメント（同一文字の長い連続: 0000...、xxxx...）を持つかを返す。
// Discord の id は数値スノーフレークで、token は英数字混在だ。そのため
// そのような値は本物の webhook では決してない。プレースホルダ扱いにすると、
// ローダーが警告し、notifier が黙って失敗せずスキップする。
// isFakeDiscordWebhook reports whether a Discord webhook URL carries an
// obviously-fake id or token segment: a long run of a single character
// (0000..., xxxx...). Discord ids are numeric snowflakes and tokens are mixed
// alphanumeric, so such a value is never a real webhook. Treating it as a
// placeholder makes the loader warn and the notifier skip it instead of
// silently failing.
func isFakeDiscordWebhook(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "discord.com", "discordapp.com", "canary.discord.com", "ptb.discord.com":
	default:
		return false
	}
	if !strings.HasPrefix(u.Path, "/api/webhooks/") {
		return false
	}
	for _, seg := range strings.Split(strings.TrimPrefix(u.Path, "/api/webhooks/"), "/") {
		if isRepeatedChar(seg, 12) {
			return true
		}
	}
	return false
}

// isRepeatedChar は、s が少なくとも n バイトで、全バイトが同一文字
// （大文字小文字を問わず）かを返す。本物の webhook id/token はこの形にならない。
// そのため同一文字の長い連続は、紛れもないダミー値だ。
// isRepeatedChar reports whether s is at least n bytes long and every byte is
// the same character (case-insensitive). Real webhook ids/tokens never look
// like this, so a long run of one character is an unambiguous dummy value.
func isRepeatedChar(s string, n int) bool {
	if len(s) < n {
		return false
	}
	first := s[0]
	for i := 1; i < len(s); i++ {
		if s[i] != first {
			return false
		}
	}
	return true
}

// validateWebhookURLShape は scheme/host/SSRF の検査を行う。
// validateWebhookURLShape performs the scheme/host/SSRF checks.
func validateWebhookURLShape(raw string) error {
	s := strings.TrimSpace(raw)
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("webhook_url を解析できません: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook_url は http または https のみ許可されます: %s", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("webhook_url にホストがありません")
	}

	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("webhook_url に内部アドレスは指定できません: %s", host)
		}
		return nil
	}

	// ホスト名: 解決した全アドレスを検査する。解決失敗はここでは致命的でない
	// （設定時に DNS が使えないことがある）。ホストは名前なので、内部リテラル
	// ではない。
	// Hostname: check every resolved address. A resolution failure is not
	// fatal here (DNS may be unavailable at config time); the host is a name,
	// so it is not an internal literal.
	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	for _, ip := range addrs {
		if isBlockedIP(ip) {
			return fmt.Errorf("webhook_url のホスト %s は内部アドレス (%s) を指しています", host, ip)
		}
	}
	return nil
}

// ValidateAgentURL は agent の dashboard_url を検証する。ws:// と wss:// のみ
// 許可する（agent は WebSocket で通信する）。loopback と private LAN アドレスは
// 意図的に許可する。既定の構成は ws://localhost:8080/ws に接続し、LAN の
// dashboard は正常な配備だからだ。拒否するのは非 WebSocket scheme（file://、
// gopher://、http://）と、リンクローカル / クラウドメタデータ範囲
// （169.254.0.0/16、fe80::/10）だ。後者は次回再起動時に agent をメタデータ
// サービスに対する SSRF/DoS の踏み台に変え得る。
// ValidateAgentURL validates the agent.s dashboard_url. Only ws:// and wss://
// are accepted (the agent speaks WebSocket). Loopback and private LAN
// addresses are intentionally allowed: the default setup connects to
// ws://localhost:8080/ws and a LAN dashboard is a normal deployment. What is
// rejected is a non-WebSocket scheme (file://, gopher://, http://) and the
// link-local / cloud-metadata range (169.254.0.0/16, fe80::/10), which would
// turn the agent into an SSRF/DoS vector against the metadata service on its
// next restart.
func ValidateAgentURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("dashboard_url が空です")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("dashboard_url を解析できません: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("dashboard_url は ws:// または wss:// のみ許可されます: %s", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("dashboard_url にホストがありません")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("dashboard_url にリンクローカル/メタデータアドレスは指定できません: %s", u.Hostname())
		}
	}
	return nil
}

// isMetadataIP は、ip がリンクローカル / クラウドメタデータの範囲に属する場合に
// true を返す。ValidateWebhookURL の isBlockedIP は loopback / private も含めて
// すべて拒否するが、log_forward は LAN コレクタ（private / loopback）を意図的に
// 許可する。ここではリンクローカル（169.254.0.0/16、fe80::/10）と既知の
// メタデータ用アドレス（100.100.100.200: Alibaba）だけを弾き、SSRF の典型
// ターゲットのみを塞ぐ。
// isMetadataIP reports whether ip belongs to a link-local / cloud-metadata
// range. ValidateWebhookURL's isBlockedIP rejects loopback and private too,
// but log_forward intentionally allows LAN collectors (private / loopback).
// This helper blocks only link-local (169.254.0.0/16, fe80::/10) and known
// metadata addresses (100.100.100.200: Alibaba), closing the SSRF-typical
// targets while keeping LAN collectors usable.
func isMetadataIP(ip net.IP) bool {
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	// Alibaba Cloud metadata service (ECS).
	if ip.Equal(net.ParseIP("100.100.100.200")) {
		return true
	}
	return false
}

// isBlockedIP は、ip が loopback、private、リンクローカル、マルチキャスト、
// unspecified、その他公開ユニキャストでない場合に true を返す。
// isBlockedIP reports whether ip is loopback, private, link-local, multicast,
// unspecified, or otherwise not a public unicast address.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 || ip4[0] >= 224 {
			return true
		}
	}
	if ip.IsInterfaceLocalMulticast() {
		return true
	}
	return false
}
