package module

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// panicInitModule panics in Init; registration must fail without crashing.
type panicInitModule struct{}

func (m *panicInitModule) Name() string                   { return "panic_init" }
func (m *panicInitModule) Description() string            { return "x" }
func (m *panicInitModule) Interval() time.Duration        { return time.Hour }
func (m *panicInitModule) Run(ctx context.Context) error  { return nil }
func (m *panicInitModule) Init(ctx context.Context) error { panic("boom in Init") }

func TestRegisterRecoversFromInitPanic(t *testing.T) {
	mgr := NewModuleManager(nil)
	if err := mgr.Register(context.Background(), &panicInitModule{}); err == nil {
		t.Fatal("Register should return an error when Init panics")
	}
	if _, ok := mgr.Get("panic_init"); ok {
		t.Fatal("a module whose Init panicked must not be registered")
	}
}

// panicRunModule panics on the first Run, then records subsequent runs so we can
// prove the loop kept going after the recovered panic.
type panicRunModule struct {
	runs int32
}

func (m *panicRunModule) Name() string                   { return "panic_run" }
func (m *panicRunModule) Description() string            { return "x" }
func (m *panicRunModule) Interval() time.Duration        { return 5 * time.Millisecond }
func (m *panicRunModule) Init(ctx context.Context) error { return nil }
func (m *panicRunModule) Run(ctx context.Context) error {
	if atomic.AddInt32(&m.runs, 1) == 1 {
		panic("boom in Run")
	}
	return nil
}

func TestRunLoopSurvivesPluginPanic(t *testing.T) {
	mgr := NewModuleManager(nil)
	mod := &panicRunModule{}
	if err := mgr.Register(context.Background(), mod); err != nil {
		t.Fatalf("Register: %v", err)
	}
	mgr.Start(context.Background())
	defer mgr.Stop()

	// The first Run panics; the loop must recover and keep running.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&mod.runs) >= 3 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run loop did not continue after a plugin panic (runs=%d)", atomic.LoadInt32(&mod.runs))
}
