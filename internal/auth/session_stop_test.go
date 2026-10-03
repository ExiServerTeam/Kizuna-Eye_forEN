package auth

import (
	"sync"
	"testing"
	"time"
)

// Stop must be idempotent and safe under concurrent calls. The previous
// select/close implementation could close stopCh twice and panic
// ("close of closed channel").
func TestSessionManagerStopConcurrent(t *testing.T) {
	m := NewSessionManager(time.Hour, time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Stop() }()
	}
	wg.Wait()

	// A later call after all goroutines finished must also not panic.
	m.Stop()
}
