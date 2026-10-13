package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// portHarness drives a PortMonitor with a scripted port list.
type portHarness struct {
	mu     sync.Mutex
	ports  []portInfo
	m      *PortMonitor
	events []module.SecurityEvent
}

func (h *portHarness) set(list []portInfo) {
	h.mu.Lock()
	h.ports = list
	h.mu.Unlock()
}

func (h *portHarness) list() ([]portInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]portInfo(nil), h.ports...), nil
}

func (h *portHarness) take() []module.SecurityEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.events
	h.events = nil
	return out
}

// newPortHarness builds a monitor with the given cooldown and a scripted list.
// It restores the global listPortsFn when the test finishes.
func newPortHarness(t *testing.T, cooldownSec int) *portHarness {
	t.Helper()
	orig := listPortsFn
	h := &portHarness{}
	listPortsFn = h.list
	t.Cleanup(func() { listPortsFn = orig })

	statePath := filepath.Join(t.TempDir(), "ports.json")
	h.m = NewPortMonitorWithCooldown(statePath, cooldownSec, "ja", nil, func(ev module.SecurityEvent) {
		h.mu.Lock()
		h.events = append(h.events, ev)
		h.mu.Unlock()
	})
	return h
}

func countNotified(events []module.SecurityEvent) int {
	n := 0
	for _, ev := range events {
		if ev.Title == "新規リッスンポートを検知" {
			n++
		}
	}
	return n
}

func countSuppressed(events []module.SecurityEvent) int {
	n := 0
	for _, ev := range events {
		if ev.Title == "新規リッスンポートの再通知を抑制" {
			n++
		}
	}
	return n
}

// TestPortRenotifyCooldownSuppressesFlaps: opening, closing and reopening the
// same port three times inside the cooldown yields one notification and two
// suppressed (info) records (task 8).
func TestPortRenotifyCooldownSuppressesFlaps(t *testing.T) {
	h := newPortHarness(t, 600)
	// baseline: no ports
	h.set(nil)
	h.m.Check()
	h.take()

	p := portInfo{Proto: "tcp", Address: "0.0.0.0", Port: 44444}
	for i := 0; i < 3; i++ {
		h.set([]portInfo{p})
		h.m.Check()
		h.set(nil)
		h.m.Check()
	}
	events := h.take()
	if got := countNotified(events); got != 1 {
		t.Errorf("notifications = %d, want 1", got)
	}
	if got := countSuppressed(events); got != 2 {
		t.Errorf("suppressed records = %d, want 2", got)
	}
	// Suppressed events must be info and carry the dedup_key/port extra.
	for _, ev := range events {
		if ev.Title != "新規リッスンポートの再通知を抑制" {
			continue
		}
		if ev.Level != "info" {
			t.Errorf("suppressed level = %q, want info", ev.Level)
		}
		if ev.Extra["dedup_key"] != "listen_port_renotify_suppressed" {
			t.Errorf("dedup_key = %q", ev.Extra["dedup_key"])
		}
		if ev.Extra["port"] != "44444" {
			t.Errorf("port extra = %q, want 44444", ev.Extra["port"])
		}
	}
}

// TestPortRenotifyAfterCooldownNotifiesAgain: once the cooldown has passed, a
// reopened port notifies again and the message notes it is a re-detection.
func TestPortRenotifyAfterCooldownNotifiesAgain(t *testing.T) {
	h := newPortHarness(t, 1) // 1-second cooldown
	h.set(nil)
	h.m.Check()
	h.take()

	p := portInfo{Proto: "tcp", Address: "0.0.0.0", Port: 55555}
	h.set([]portInfo{p})
	h.m.Check()
	h.set(nil)
	h.m.Check()
	if got := countNotified(h.take()); got != 1 {
		t.Fatalf("first open: notifications = %d, want 1", got)
	}

	// Wait out the cooldown, then reopen.
	time.Sleep(1100 * time.Millisecond)
	h.set([]portInfo{p})
	h.m.Check()
	events := h.take()
	if got := countNotified(events); got != 1 {
		t.Fatalf("after cooldown: notifications = %d, want 1", got)
	}
	// The re-detection message must carry the note.
	found := false
	for _, ev := range events {
		if ev.Title == "新規リッスンポートを検知" && ev.Message != "" {
			if contains(ev.Message, "クールダウン経過後の再検知") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("re-detection message must note the cooldown; events=%+v", events)
	}
}

// TestPortRenotifyDisabledNotifiesEveryTime: cooldown=0 keeps the old
// behaviour (every reopen notifies).
func TestPortRenotifyDisabledNotifiesEveryTime(t *testing.T) {
	h := newPortHarness(t, 0)
	h.set(nil)
	h.m.Check()
	h.take()

	p := portInfo{Proto: "tcp", Address: "0.0.0.0", Port: 66666}
	for i := 0; i < 3; i++ {
		h.set([]portInfo{p})
		h.m.Check()
		h.set(nil)
		h.m.Check()
	}
	events := h.take()
	if got := countNotified(events); got != 3 {
		t.Errorf("notifications = %d, want 3 (cooldown disabled)", got)
	}
	if got := countSuppressed(events); got != 0 {
		t.Errorf("suppressed = %d, want 0", got)
	}
}

// contains is a tiny substring helper so the test does not import strings just
// for one call.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
