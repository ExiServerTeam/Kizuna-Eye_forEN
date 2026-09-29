package auth

import (
	"sync"
	"testing"
	"time"
)

// Stop must be safe to call more than once (idempotent) and must not panic.
func TestLoginLimiterStopIdempotent(t *testing.T) {
	l := newLoginLimiter(3, time.Minute, time.Minute)
	l.Stop()
	l.Stop() // second call must not panic (sync.Once)
}

// Stop can be called concurrently without panicking.
func TestLoginLimiterStopConcurrent(t *testing.T) {
	l := newLoginLimiter(3, time.Minute, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); l.Stop() }()
	}
	wg.Wait()
}

// Handler.Stop stops both limiters without panic.
func TestHandlerStop(t *testing.T) {
	m := NewSessionManager(time.Hour, time.Hour)
	defer m.Stop()
	h := NewHandler(nil, m, nil, false, true, "")
	h.Stop()
	h.Stop()
}
