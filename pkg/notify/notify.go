package notify

import (
	"context"
	"strings"
	"time"
)

// ============================================================
// Level is the notification severity.
// ============================================================
type Level string

const (
	LevelCritical Level = "critical" // red
	LevelWarning  Level = "warning"  // yellow
	LevelSuccess  Level = "success"  // green
	LevelInfo     Level = "info"     // blue
)

// ============================================================
// Alert is a single notification.
// ============================================================
type Alert struct {
	Type      string    // internal identifier ("memory", "disk", "cpu_temp", ...)
	Level     Level     // severity
	Icon      string    // emoji
	Title     string    // title (without icon)
	Message   string    // body
	Timestamp time.Time // occurred at
}

// Color returns the color code used by Discord embeds.
func (a *Alert) Color() uint32 {
	switch a.Level {
	case LevelCritical:
		return 0xEF4444 // red
	case LevelWarning:
		return 0xF59E0B // orange
	case LevelSuccess:
		return 0x22C55E // green
	case LevelInfo:
		return 0x3B82F6 // blue
	default:
		return 0x95A5A6 // gray
	}
}

// FullTitle returns the title with the icon (if any).
func (a *Alert) FullTitle() string {
	return a.IconPrefix() + a.Title
}

// IconPrefix returns the icon followed by a space, or "" when there is no icon.
func (a *Alert) IconPrefix() string {
	if a.Icon == "" {
		return ""
	}
	return a.Icon + " "
}

// FullMessage returns the body prefixed with "[Kizuna-Eye] ".
func (a *Alert) FullMessage() string {
	return "[Kizuna-Eye] " + a.Message
}

// escapeSlackMrkdwn neutralises Slack mrkdwn control characters in
// untrusted text. Alert messages can contain attacker-controlled data
// (SSH usernames, IPs parsed from auth.log), and Slack mrkdwn would turn
// <!channel> / <@user> / <url|text> into live mentions or links.
func escapeSlackMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// truncateRunes shortens s to at most n runes (never splitting a multi-byte
// character), appending an ellipsis when the string was cut.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return string(r[:1])
	}
	return string(r[:n-1]) + "…"
}

// ============================================================
// Notifier is implemented by notification channels.
// ============================================================
type Notifier interface {
	// Name returns the channel name (for logs).
	Name() string
	// Send sends the notification.
	Send(ctx context.Context, a *Alert) error
}
