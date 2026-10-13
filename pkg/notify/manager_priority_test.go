package notify

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// blockingNotifier blocks until released, so the manager's send slots stay full.
type blockingNotifier struct {
	release  chan struct{}
	sends    int32
	observed int32
}

func (b *blockingNotifier) Name() string { return "blocking" }
func (b *blockingNotifier) Send(ctx context.Context, a *Alert) error {
	atomic.AddInt32(&b.observed, 1)
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	atomic.AddInt32(&b.sends, 1)
	return nil
}

// A non-critical alert must be dropped when the semaphore is saturated, and
// must not block the caller.
func TestNotifyDropsNonCriticalWhenSaturated(t *testing.T) {
	m := &Manager{
		notifiers: []Notifier{},
		enabled:   true,
		sem:       make(chan struct{}, 1),
	}
	release := make(chan struct{})
	n := &blockingNotifier{release: release}
	m.notifiers = []Notifier{n}

	// Fill the single slot with a long-running send.
	m.sem <- struct{}{}
	go func() { time.Sleep(50 * time.Millisecond); <-m.sem }()

	done := make(chan struct{})
	go func() {
		m.Notify(&Alert{Level: LevelWarning, Message: "noise"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("non-critical Notify blocked on a saturated manager")
	}

	close(release)
}

// A critical alert must wait for a free slot (bounded) rather than being
// dropped immediately.
func TestNotifyCriticalWaitsForSlot(t *testing.T) {
	m := &Manager{
		notifiers: []Notifier{},
		enabled:   true,
		sem:       make(chan struct{}, 1),
	}
	release := make(chan struct{})
	n := &blockingNotifier{release: release}
	m.notifiers = []Notifier{n}

	// Occupy the slot briefly, then free it.
	m.sem <- struct{}{}
	go func() {
		time.Sleep(100 * time.Millisecond)
		<-m.sem
	}()

	m.Notify(&Alert{Level: LevelCritical, Message: "fire"})

	// Wait for the send to START (Send is entered). It blocks on release, so
	// sends (incremented on completion) stays 0 until we release it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&n.observed) == 1 {
			close(release)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	t.Fatal("critical alert was not sent even though a slot became free")
}
