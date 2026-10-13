package auth

import (
	"testing"
	"time"
)

// Regression: after a lockout expires, the failure counter must reset.
// Otherwise the very next failure while the original window is still open
// would immediately re-lock the IP.
func TestLoginLimiterLockoutExpiryResetsCounter(t *testing.T) {
	l := newLoginLimiter(2, time.Minute, 20*time.Millisecond)
	ip := "4.4.4.4"

	l.RecordFailure(ip)
	l.RecordFailure(ip)
	if l.Allow(ip) {
		t.Fatal("IP should be locked out after reaching the limit")
	}

	// Wait for the lockout to expire.
	time.Sleep(40 * time.Millisecond)
	if !l.Allow(ip) {
		t.Fatal("IP should be allowed once the lockout expired")
	}

	// A single failure after the lockout must not immediately re-lock the IP.
	l.RecordFailure(ip)
	if !l.Allow(ip) {
		t.Fatal("one failure after lockout must not re-lock the IP (counter should have reset)")
	}
}
