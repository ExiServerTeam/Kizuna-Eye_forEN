package main

import (
	"testing"

	"Kizuna-Eye/pkg/module"
)

// 回帰テスト: 送信に失敗したイベントを Requeue で先頭に戻し、順序と上限を保つ。
func TestRequeueRestoresOrderAndCap(t *testing.T) {
	cfg := DefaultConfig()
	m := NewMonitor(cfg, nil, nil)

	// 既存キューに1件。
	m.queue = []module.SecurityEvent{{Title: "queued"}}
	// 送信に失敗したイベント2件を先頭へ戻す。
	m.Requeue([]module.SecurityEvent{{Title: "failed-1"}, {Title: "failed-2"}})

	if len(m.queue) != 3 {
		t.Fatalf("queue len = %d, want 3", len(m.queue))
	}
	if m.queue[0].Title != "failed-1" || m.queue[1].Title != "failed-2" || m.queue[2].Title != "queued" {
		t.Fatalf("order broken: %+v", m.queue)
	}

	// 上限を超えても maxQueueSize を超えない。
	big := make([]module.SecurityEvent, maxQueueSize+100)
	m.Requeue(big)
	if len(m.queue) > maxQueueSize {
		t.Fatalf("queue grew beyond cap: %d", len(m.queue))
	}
}
