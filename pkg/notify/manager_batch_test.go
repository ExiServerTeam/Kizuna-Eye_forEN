package notify

import (
	"context"
	"sync"
	"testing"
	"time"

	"Kizuna-Eye/pkg/config"
)

// recordingNotifier counts Send calls and keeps the alerts it received.
type recordingNotifier struct {
	mu     sync.Mutex
	count  int
	alerts []*Alert
}

func (r *recordingNotifier) Name() string { return "recording" }
func (r *recordingNotifier) Send(ctx context.Context, a *Alert) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
	r.alerts = append(r.alerts, a)
	return nil
}
func (r *recordingNotifier) snapshot() (int, []*Alert) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count, append([]*Alert(nil), r.alerts...)
}

// newBatchManager builds a Manager with batching enabled and a recording
// notifier, bypassing FromConfig so the test controls the window.
func newBatchManager(window time.Duration, excludeCritical bool) (*Manager, *recordingNotifier) {
	rn := &recordingNotifier{}
	m := &Manager{
		notifiers:            []Notifier{rn},
		enabled:              true,
		sem:                  make(chan struct{}, maxConcurrentSends),
		batchEnabled:         true,
		batchWindow:          window,
		batchExcludeCritical: excludeCritical,
	}
	return m, rn
}

// TestBatchAggregatesAlerts verifies that N alerts within the window become
// one message (the history is recorded separately by the caller).
func TestBatchAggregatesAlerts(t *testing.T) {
	m, rn := newBatchManager(80*time.Millisecond, true)
	for i := 0; i < 5; i++ {
		m.Notify(&Alert{Type: "security_integrity", Level: LevelWarning, Title: "改ざん", Message: "/tmp/f", Timestamp: time.Now()})
	}
	// Wait for the window to fire.
	time.Sleep(250 * time.Millisecond)

	count, alerts := rn.snapshot()
	if count != 1 {
		t.Fatalf("send count = %d, want 1 (batched)", count)
	}
	if alerts[0].Type != "batch" {
		t.Fatalf("batched alert type = %q, want \"batch\"", alerts[0].Type)
	}
}

// TestBatchExcludesCritical verifies critical alerts bypass the batch and go
// out immediately (so a single critical is never delayed).
func TestBatchExcludesCritical(t *testing.T) {
	m, rn := newBatchManager(200*time.Millisecond, true)
	m.Notify(&Alert{Type: "security_cron", Level: LevelCritical, Title: "cron", Message: "x", Timestamp: time.Now()})
	// Critical should be sent without waiting for the window.
	time.Sleep(50 * time.Millisecond)
	count, alerts := rn.snapshot()
	if count != 1 {
		t.Fatalf("critical send count = %d, want 1 immediately", count)
	}
	if alerts[0].Level != LevelCritical {
		t.Fatalf("level = %s, want critical", alerts[0].Level)
	}
}

// TestBatchSingleAlertSendsImmediately verifies a lone alert is not wrapped in
// a batch summary (no pointless indirection for one item).
func TestBatchSingleAlertSendsImmediately(t *testing.T) {
	m, rn := newBatchManager(60*time.Millisecond, true)
	m.Notify(&Alert{Type: "security_listen_port", Level: LevelWarning, Title: "port", Message: "x", Timestamp: time.Now()})
	time.Sleep(200 * time.Millisecond)
	count, alerts := rn.snapshot()
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if alerts[0].Type != "security_listen_port" {
		t.Fatalf("type = %q, want the original (not a batch)", alerts[0].Type)
	}
}

// TestBatchFlushOnShutdown verifies Flush sends pending alerts immediately.
func TestBatchFlushOnShutdown(t *testing.T) {
	m, rn := newBatchManager(10*time.Second, true) // long window
	m.Notify(&Alert{Type: "security_suid", Level: LevelWarning, Title: "suid", Message: "x", Timestamp: time.Now()})
	m.Notify(&Alert{Type: "security_suid", Level: LevelWarning, Title: "suid", Message: "y", Timestamp: time.Now()})
	m.Flush()
	// 送信は非同期（sendAsync の goroutine）。完了を待ってから数える。
	time.Sleep(100 * time.Millisecond)
	count, _ := rn.snapshot()
	if count != 1 {
		t.Fatalf("after flush count = %d, want 1", count)
	}
}

// TestBatchDisabledSendsPerAlert verifies the batch toggle is honored.
func TestBatchDisabledSendsPerAlert(t *testing.T) {
	rn := &recordingNotifier{}
	m := &Manager{
		notifiers:    []Notifier{rn},
		enabled:      true,
		sem:          make(chan struct{}, maxConcurrentSends),
		batchEnabled: false,
	}
	for i := 0; i < 3; i++ {
		m.Notify(&Alert{Type: "t", Level: LevelWarning, Title: "x", Message: "y", Timestamp: time.Now()})
	}
	// Give the async goroutines a moment.
	time.Sleep(100 * time.Millisecond)
	count, _ := rn.snapshot()
	if count != 3 {
		t.Fatalf("count = %d, want 3 (batching disabled)", count)
	}
}

// TestFromConfigBatchDefaults verifies the config defaults are applied.
func TestFromConfigBatchDefaults(t *testing.T) {
	trueVal := true
	cfg := config.NotificationsConfig{
		Enabled:        true,
		BatchEnabled:   &trueVal,
		BatchWindowSec: 0, // should default to 5
	}
	m := FromConfig(cfg, nil)
	if m.batchWindow != 5*time.Second {
		t.Fatalf("batchWindow = %s, want 5s", m.batchWindow)
	}
	if !m.batchExcludeCritical {
		t.Fatal("batchExcludeCritical should default to true when nil")
	}
}
