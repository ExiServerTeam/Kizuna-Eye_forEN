package alert

import (
	"testing"
	"time"

	"Kizuna-Eye/pkg/status"
)

// TestAgentDisconnectCooldown verifies that a flapping agent connection does
// not spam notifications: a disconnect within the cooldown produces neither a
// disconnect alert nor a later recovery alert.
func TestAgentDisconnectCooldown(t *testing.T) {
	// 他メトリクスが同時発火しないよう閾値を無効化する。
	cfg := Config{
		MemoryWarn:     1000,
		MemoryCritical: 1000,
		Cooldown:       time.Hour,
		RecoveryHold:   time.Second,
		NotifyRecovery: true,
	}
	e := NewEngine(cfg, nil, nil)
	st := &status.SystemStatus{Timestamp: time.Now().Unix()}

	// 1回目の切断 -> 通知（履歴1件）
	e.OnStatus(st)
	e.OnAgentDisconnect()
	if got := len(e.Alerts()); got != 1 {
		t.Fatalf("first disconnect: want 1 entry, got %d", got)
	}

	// 復帰 -> 復帰通知（履歴2件）
	e.OnStatus(st)
	if got := len(e.Alerts()); got != 2 {
		t.Fatalf("recovery: want 2 entries, got %d", got)
	}

	// クールダウン内の2回目の切断 -> 通知なし（履歴は2件のまま）
	e.OnAgentDisconnect()
	if got := len(e.Alerts()); got != 2 {
		t.Fatalf("second disconnect within cooldown: want 2 entries, got %d", got)
	}

	// クールダウン内では復帰通知も出ない（履歴は2件のまま）
	e.OnStatus(st)
	if got := len(e.Alerts()); got != 2 {
		t.Fatalf("recovery within cooldown: want 2 entries, got %d", got)
	}
}

// TestAgentTimeoutCooldown verifies the same throttling applies to the
// periodic agent-timeout check.
func TestAgentTimeoutCooldown(t *testing.T) {
	// 他メトリクスが同時発火しないよう閾値を無効化する。
	cfg := Config{
		MemoryWarn:     1000,
		MemoryCritical: 1000,
		Cooldown:       time.Hour,
		AgentTimeout:   time.Millisecond,
	}
	e := NewEngine(cfg, nil, nil)

	e.OnStatus(&status.SystemStatus{Timestamp: time.Now().Unix()})

	// タイムアウト検知 -> 通知（履歴1件）
	time.Sleep(5 * time.Millisecond)
	e.CheckAgentTimeout()
	if got := len(e.Alerts()); got != 1 {
		t.Fatalf("first timeout: want 1 entry, got %d", got)
	}

	// クールダウン内の再検知 -> 通知なし
	e.OnStatus(&status.SystemStatus{Timestamp: time.Now().Unix()})
	time.Sleep(5 * time.Millisecond)
	e.CheckAgentTimeout()
	if got := len(e.Alerts()); got != 1 {
		t.Fatalf("second timeout within cooldown: want 1 entry, got %d", got)
	}
}
