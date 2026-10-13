package main

import (
	"context"
	"testing"
	"time"

	"Kizuna-Eye/pkg/module"
)

// panickingNameModule panics in Name(). safeModuleName / safePluginName must
// absorb it so the agent does not crash while resolving a plugin's name
// (disabled-branch resolution and post-registration name read).
type panickingNameModule struct{}

func (m *panickingNameModule) Name() string                   { panic("boom in Name") }
func (m *panickingNameModule) Description() string            { return "x" }
func (m *panickingNameModule) Interval() time.Duration        { return time.Hour }
func (m *panickingNameModule) Init(ctx context.Context) error { return nil }
func (m *panickingNameModule) Run(ctx context.Context) error  { return nil }

func TestSafeModuleNameAbsorbsPanic(t *testing.T) {
	if got := safeModuleName(&panickingNameModule{}, nil); got != "" {
		t.Fatalf("safeModuleName should return empty on panic, got %q", got)
	}
}

func TestSafePluginNameAbsorbsPanic(t *testing.T) {
	if got := safePluginName(&panickingNameModule{}, nil); got != "" {
		t.Fatalf("safePluginName should return empty on panic, got %q", got)
	}
}

// panickingRequeuer implements SecurityEventRequeuer and panics when asked to
// put events back. requeueSecurityEventsSafe must absorb it: this runs in the
// agent's main send loop, so an unabsorbed panic would kill the agent.
type panickingRequeuer struct{}

func (p *panickingRequeuer) RequeueSecurityEvents([]module.SecurityEvent) { panic("boom in Requeue") }

func TestRequeueSecurityEventsSafeAbsorbsPanic(t *testing.T) {
	// Must not panic (which would fail the test by crashing the process).
	requeueSecurityEventsSafe(&panickingRequeuer{}, []module.SecurityEvent{{Title: "x"}}, nil)
}
