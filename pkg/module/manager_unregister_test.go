package module

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// countingModule counts how many times Run is called.
type countingModule struct {
	runs int32
}

func (m *countingModule) Name() string                   { return "counting" }
func (m *countingModule) Description() string            { return "test" }
func (m *countingModule) Interval() time.Duration        { return 10 * time.Millisecond }
func (m *countingModule) Init(ctx context.Context) error { return nil }
func (m *countingModule) Run(ctx context.Context) error  { atomic.AddInt32(&m.runs, 1); return nil }

// Unregister must stop the module's run loop so it no longer runs.
func TestUnregisterStopsModule(t *testing.T) {
	mgr := NewModuleManager(nil)
	mod := &countingModule{}
	if err := mgr.Register(context.Background(), mod); err != nil {
		t.Fatalf("Register: %v", err)
	}
	mgr.Start(context.Background())

	// Let it run a few cycles.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&mod.runs) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&mod.runs) < 1 {
		t.Fatal("module never ran")
	}

	if err := mgr.Unregister("counting"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if _, ok := mgr.Get("counting"); ok {
		t.Fatal("module should be removed after Unregister")
	}

	// Record the count, wait, and confirm it does not keep growing.
	snapshot := atomic.LoadInt32(&mod.runs)
	time.Sleep(60 * time.Millisecond)
	if got := atomic.LoadInt32(&mod.runs); got != snapshot {
		t.Fatalf("module kept running after Unregister: %d -> %d", snapshot, got)
	}

	mgr.Stop()
}

// Unregister of an unknown module is not an error (idempotent).
func TestUnregisterUnknownIsNoop(t *testing.T) {
	mgr := NewModuleManager(nil)
	if err := mgr.Unregister("nope"); err != nil {
		t.Fatalf("Unregister(unknown) = %v, want nil", err)
	}
}
