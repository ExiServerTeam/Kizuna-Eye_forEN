package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// loginLimiter throttles failed login attempts per client IP.
// It is a simple in-memory sliding window: after maxFailures failures
// within window, the IP is locked out for lockDuration.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptInfo
	max      int
	window   time.Duration
	lockout  time.Duration

	stopCh chan struct{}
	stopOn sync.Once
}

type attemptInfo struct {
	count      int
	firstAt    time.Time
	lockedTill time.Time
}

func newLoginLimiter(max int, window, lockout time.Duration) *loginLimiter {
	if max <= 0 {
		max = 10
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	if lockout <= 0 {
		lockout = 5 * time.Minute
	}
	l := &loginLimiter{
		attempts: make(map[string]*attemptInfo),
		max:      max,
		window:   window,
		lockout:  lockout,
		stopCh:   make(chan struct{}),
	}
	go l.reaper()
	return l
}

// Stop terminates the reaper goroutine. It is idempotent.
func (l *loginLimiter) Stop() {
	l.stopOn.Do(func() { close(l.stopCh) })
}

// clientIP extracts the client IP for rate limiting.
//
// X-Forwarded-For is only honored when the request arrives from a loopback
// address, i.e. a reverse proxy on the same host. Trusting XFF from an
// arbitrary peer would let an attacker bypass the login rate limit by
// sending a different X-Forwarded-For value on every request.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	// Only a loopback peer (local reverse proxy) may set X-Forwarded-For.
	if isLoopback(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// X-Forwarded-For may be a comma-separated list; the first is the client.
			for i := 0; i < len(xff); i++ {
				if xff[i] == ',' {
					xff = xff[:i]
					break
				}
			}
			if ip := net.ParseIP(trimSpace(xff)); ip != nil {
				return ip.String()
			}
		}
	}
	return host
}

// isLoopback reports whether host is a loopback address (127.0.0.0/8 or ::1).
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// Allow reports whether a login attempt from ip may proceed.
func (l *loginLimiter) Allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	info, ok := l.attempts[ip]
	if !ok {
		return true
	}
	if now.Before(info.lockedTill) {
		return false
	}
	// Lockout expired: clear the failure counter so one later failure does
	// not immediately hit the threshold again while the old window is still
	// open.
	if !info.lockedTill.IsZero() {
		info.count = 0
		info.firstAt = now
		info.lockedTill = time.Time{}
		return true
	}
	// Window expired: reset the counter.
	if now.Sub(info.firstAt) > l.window {
		delete(l.attempts, ip)
		return true
	}
	return true
}

// RecordFailure increments the failure count and locks out when the
// threshold is reached.
func (l *loginLimiter) RecordFailure(ip string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	info, ok := l.attempts[ip]
	if !ok || now.Sub(info.firstAt) > l.window {
		info = &attemptInfo{count: 0, firstAt: now}
		l.attempts[ip] = info
	}
	info.count++
	if info.count >= l.max {
		info.lockedTill = now.Add(l.lockout)
	}
}

// Hit records one attempt against ip and locks out when the limit is reached.
// It is RecordFailure under a name that also fits successful-but-abusive
// calls (e.g. guest-login spam) that should still be rate limited.
func (l *loginLimiter) Hit(ip string) {
	l.RecordFailure(ip)
}

// Reset clears the failure record for ip after a successful login.
func (l *loginLimiter) Reset(ip string) {
	l.mu.Lock()
	delete(l.attempts, ip)
	l.mu.Unlock()
}

// reaper periodically removes stale entries until Stop is called.
func (l *loginLimiter) reaper() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			l.mu.Lock()
			for ip, info := range l.attempts {
				if now.After(info.lockedTill) && now.Sub(info.firstAt) > l.window {
					delete(l.attempts, ip)
				}
			}
			l.mu.Unlock()
		}
	}
}
