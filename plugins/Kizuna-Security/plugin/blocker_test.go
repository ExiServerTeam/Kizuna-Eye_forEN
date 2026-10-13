package main

import (
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

func newTestBlocker(mode string, whitelist []string) (*blocker, *[]module.SecurityEvent) {
	cfg := DefaultConfig()
	cfg.BlockMode = mode
	cfg.BlockWhitelist = whitelist
	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }
	b := newBlocker(cfg, nil, emit)
	return b, &events
}

func TestBlockerOffEmitsNothing(t *testing.T) {
	b, events := newTestBlocker("off", nil)
	b.HandleBlockCandidate("203.0.113.5", 5)
	if len(*events) != 0 {
		t.Fatalf("off mode should emit nothing: %+v", *events)
	}
}

func TestBlockerDryRunEmitsWarning(t *testing.T) {
	b, events := newTestBlocker("dry-run", nil)
	b.HandleBlockCandidate("203.0.113.5", 5)
	if len(*events) != 1 || (*events)[0].Category != "block" || (*events)[0].Level != "warning" {
		t.Fatalf("dry-run should emit one warning block event: %+v", *events)
	}
}

func TestBlockerWhitelistSkips(t *testing.T) {
	b, events := newTestBlocker("dry-run", []string{"203.0.113.0/24"})
	b.HandleBlockCandidate("203.0.113.5", 5)
	if len(*events) != 0 {
		t.Fatalf("whitelisted IP should not be reported: %+v", *events)
	}
}

func TestBlockerLoopbackSkips(t *testing.T) {
	b, events := newTestBlocker("dry-run", nil)
	b.HandleBlockCandidate("127.0.0.1", 5)
	if len(*events) != 0 {
		t.Fatalf("loopback should never be blocked: %+v", *events)
	}
}

func TestBlockerInvalidIPSkips(t *testing.T) {
	b, events := newTestBlocker("dry-run", nil)
	b.HandleBlockCandidate("not-an-ip", 5)
	if len(*events) != 0 {
		t.Fatalf("invalid IP should be skipped: %+v", *events)
	}
}

// enforce でも whitelist は絶対にブロックしない（iptables を実行しない）。
func TestBlockerEnforceWhitelistNoExec(t *testing.T) {
	b, events := newTestBlocker("enforce", []string{"192.168.0.176"})
	b.HandleBlockCandidate("192.168.0.176", 5)
	if len(*events) != 0 {
		t.Fatalf("whitelisted IP must not be blocked: %+v", *events)
	}
	if len(b.blocked) != 0 {
		t.Fatalf("nothing should be marked as blocked")
	}
}

// blocked マップが上限を超えないことを検証する（メモリ枯渇対策）。
func TestBlockerBlockedMapLimit(t *testing.T) {
	b, _ := newTestBlocker("dry-run", nil)
	// enforce 相当の内部マップを直接検査する。
	b.mu.Lock()
	for i := 0; i < maxBlockedIPs+100; i++ {
		if len(b.blocked) >= maxBlockedIPs {
			b.evictBlockedLocked(time.Now())
		}
		b.blocked["10."+itoa(i/65536)+"."+itoa((i/256)%256)+"."+itoa(i%256)] = time.Now().Add(time.Hour)
	}
	size := len(b.blocked)
	b.mu.Unlock()
	if size > maxBlockedIPs {
		t.Fatalf("blocked map grew beyond limit: %d", size)
	}
}

// isValidIPArg は exec 引数インジェクションを防ぐ。
func TestIsValidIPArg(t *testing.T) {
	valid := []string{"203.0.113.5", "2001:db8::1"}
	for _, s := range valid {
		if !isValidIPArg(s) {
			t.Errorf("should be valid: %q", s)
		}
	}
	bad := []string{"", "-j", "1.2.3.4; rm -rf /", "1.2.3.4 -j DROP", "not-an-ip"}
	for _, s := range bad {
		if isValidIPArg(s) {
			t.Errorf("should be invalid: %q", s)
		}
	}
}
