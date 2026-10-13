package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

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

// ValidateAgentURL validates the agent's dashboard_url. Only ws:// and wss://
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
