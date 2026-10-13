package module

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// panicNameModule panics in Name(). Register must reject it without crashing
// the process (Name() is the first plugin method Register touches).
type panicNameModule struct{}

func (m *panicNameModule) Name() string                   { panic("boom in Name") }
func (m *panicNameModule) Description() string            { return "x" }
func (m *panicNameModule) Interval() time.Duration        { return time.Hour }
func (m *panicNameModule) Run(ctx context.Context) error  { return nil }
func (m *panicNameModule) Init(ctx context.Context) error { return nil }

func TestRegisterRejectsPanickingName(t *testing.T) {
	mgr := NewModuleManager(nil)
	if err := mgr.Register(context.Background(), &panicNameModule{}); err == nil {
		t.Fatal("Register must return an error when Name() panics, not crash")
	}
}

// panicIntervalModule has a working Name() but panics in Interval(). The run
// loop calls Interval() in its own goroutine; without recovery that panic would
// terminate the whole process.
type panicIntervalModule struct {
	runs int32
}

func (m *panicIntervalModule) Name() string                   { return "panic_interval" }
func (m *panicIntervalModule) Description() string            { return "x" }
func (m *panicIntervalModule) Interval() time.Duration        { panic("boom in Interval") }
func (m *panicIntervalModule) Init(ctx context.Context) error { return nil }
func (m *panicIntervalModule) Run(ctx context.Context) error {
	atomic.AddInt32(&m.runs, 1)
	return nil
}

func TestRunLoopSurvivesPanickingInterval(t *testing.T) {
	mgr := NewModuleManager(nil)
	mod := &panicIntervalModule{}
	if err := mgr.Register(context.Background(), mod); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Start launches the run loop, which immediately calls Interval(). A
	// panic there must be recovered (treated as interval 0 -> loop exits)
	// rather than crashing the process.
	mgr.Start(context.Background())
	defer mgr.Stop()

	// If we reach here without the process dying, the panic was recovered.
	// Run() must NOT have been called (Interval() panicked before the loop).
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&mod.runs) != 0 {
		t.Fatalf("Run should not execute when Interval() panics (runs=%d)", mod.runs)
	}
}
