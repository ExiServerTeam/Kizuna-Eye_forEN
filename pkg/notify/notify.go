package notify

import (
	"context"
	"time"
)

// ============================================================
// Level は通知の重要度
// ============================================================
type Level string

const (
	LevelCritical Level = "critical" // 赤
	LevelWarning  Level = "warning"  // 黄
	LevelSuccess  Level = "success"  // 緑
	LevelInfo     Level = "info"     // 青
)

// ============================================================
// Alert は1件の通知内容
// ============================================================
type Alert struct {
	Type      string    // 内部識別子 ("memory", "disk", "cpu_temp", ...)
	Level     Level     // 重要度
	Icon      string    // 絵文字
	Title     string    // タイトル（アイコン含まない）
	Message   string    // 本文
	Timestamp time.Time // 発生日時
}

// Color は Discord Embed 等で使う色コードを返す
func (a *Alert) Color() uint32 {
	switch a.Level {
	case LevelCritical:
		return 0xEF4444 // 赤
	case LevelWarning:
		return 0xF59E0B // オレンジ
	case LevelSuccess:
		return 0x22C55E // 緑
	case LevelInfo:
		return 0x3B82F6 // 青
	default:
		return 0x95A5A6 // グレー
	}
}

// FullTitle はアイコン付きタイトルを返す
func (a *Alert) FullTitle() string {
	if a.Icon == "" {
		return a.Title
	}
	return a.Icon + " " + a.Title
}

// FullMessage は "[Kizuna-Eye] " プレフィックス付きの本文を返す
func (a *Alert) FullMessage() string {
	return "[Kizuna-Eye] " + a.Message
}

// ============================================================
// Notifier は通知チャンネルが実装すべきインターフェース
// ============================================================
type Notifier interface {
	// Name は通知チャンネル名（ログ用）
	Name() string
	// Send は通知を送信する
	Send(ctx context.Context, a *Alert) error
}
