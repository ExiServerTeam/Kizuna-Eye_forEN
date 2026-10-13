package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginLimiterLockout(t *testing.T) {
	l := newLoginLimiter(3, time.Minute, time.Minute)
	ip := "1.2.3.4"

	for i := 0; i < 3; i++ {
		if !l.Allow(ip) {
			t.Fatalf("attempt %d should be allowed", i)
		}
		l.RecordFailure(ip)
	}

	if l.Allow(ip) {
		t.Fatal("after 3 failures the IP should be locked out")
	}
}

func TestLoginLimiterHitLocksOut(t *testing.T) {
	l := newLoginLimiter(3, time.Minute, time.Minute)
	ip := "9.9.9.9"
	for i := 0; i < 3; i++ {
		if !l.Allow(ip) {
			t.Fatalf("attempt %d should be allowed", i)
		}
		l.Hit(ip)
	}
	if l.Allow(ip) {
		t.Fatal("Hit should lock out after reaching the limit")
	}
}

func TestLoginLimiterReset(t *testing.T) {
	l := newLoginLimiter(3, time.Minute, time.Minute)
	ip := "5.6.7.8"
	l.RecordFailure(ip)
	l.RecordFailure(ip)
	l.Reset(ip)

	// After reset, the counter starts over.
	l.RecordFailure(ip)
	l.RecordFailure(ip)
	if !l.Allow(ip) {
		t.Fatal("reset should clear the failure count")
	}
}

func TestLoginLimiterPerIP(t *testing.T) {
	l := newLoginLimiter(2, time.Minute, time.Minute)
	l.RecordFailure("a")
	l.RecordFailure("a")
	if l.Allow("a") {
		t.Fatal("IP a should be locked")
	}
	if !l.Allow("b") {
		t.Fatal("IP b must not be affected by IP a's failures")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:12345"
	if got := clientIP(r); got != "10.0.0.5" {
		t.Fatalf("clientIP = %q, want 10.0.0.5", got)
	}
}

// A remote peer must NOT be able to spoof X-Forwarded-For to bypass the
// login rate limit.
func TestClientIPIgnoresXFFFromRemote(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:12345"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := clientIP(r); got != "10.0.0.5" {
		t.Fatalf("clientIP should ignore XFF from a remote peer: got %q, want 10.0.0.5", got)
	}
}

// A loopback peer (local reverse proxy) may set X-Forwarded-For.
func TestClientIPHonorsXFFFromLoopback(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Fatalf("clientIP with XFF from loopback = %q, want 203.0.113.7", got)
	}
}
