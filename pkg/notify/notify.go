package notify

import (
	"context"
	"strings"
	"time"
)

// ============================================================
// Level は通知の重要度だ。
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
// Alert は 1 件の通知だ。
// Alert is a single notification.
// ============================================================
type Alert struct {
	Type      string    // 内部識別子（"memory"、"disk"、"cpu_temp" など） / internal identifier ("memory", "disk", "cpu_temp", ...)
	Level     Level     // 重要度 / severity
	Icon      string    // 絵文字 / emoji
	Title     string    // タイトル（アイコンなし） / title (without icon)
	Message   string    // 本文 / body
	Timestamp time.Time // 発生日時 / occurred at

	// TitleEN / MessageEN は Title / Message の英語レンダリングだ。
	// アラート履歴がこれらを保存し、UI がキーから文字列を再生成せずに英語へ
	// 切り替えられるようにする。空は「英語レンダリング無し」を意味し、
	// UI は Title / Message にフォールバックする。
	// TitleEN / MessageEN are the English renderings of Title / Message.
	// The alert history stores them so the UI can be switched to English
	// without re-deriving the string from a key. Empty means "no English
	// rendering"; the UI falls back to Title / Message.
	TitleEN   string
	MessageEN string
}

// Color は Discord embed が使う色コードを返す。
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

// FullTitle はアイコン（あれば）付きのタイトルを返す。
// FullTitle returns the title with the icon (if any).
func (a *Alert) FullTitle() string {
	return a.IconPrefix() + a.Title
}

// IconPrefix はアイコンと空白を返し、アイコンが無ければ "" を返す。
// IconPrefix returns the icon followed by a space, or "" when there is no icon.
func (a *Alert) IconPrefix() string {
	if a.Icon == "" {
		return ""
	}
	return a.Icon + " "
}

// FullMessage は本文の先頭に "[Kizuna-Eye] " を付けて返す。
// FullMessage returns the body prefixed with "[Kizuna-Eye] ".
func (a *Alert) FullMessage() string {
	return "[Kizuna-Eye] " + a.Message
}

// escapeSlackMrkdwn は信頼できないテキスト中の Slack mrkdwn 制御文字を無害化する。
// アラートメッセージは攻撃者が制御し得るデータ（SSH ユーザー名、auth.log から
// 解析した IP）を含み得る。Slack mrkdwn は <!channel> / <@user> / <url|text> を
// 生きたメンションやリンクに変えてしまう。
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

// truncateRunes は s を最大 n ルーンへ短縮する（マルチバイト文字を決して
// 分割しない）。文字列が切られた場合は省略記号を付ける。
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
// Notifier は通知チャンネルが実装する。
// Notifier is implemented by notification channels.
// ============================================================
type Notifier interface {
	// Name はチャンネル名を返す（ログ用）。
	// Name returns the channel name (for logs).
	Name() string
	// Send は通知を送る。
	// Send sends the notification.
	Send(ctx context.Context, a *Alert) error
}
