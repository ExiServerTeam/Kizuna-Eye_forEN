package api

import (
	"testing"
	"time"
)

func newTestPluginManager() *PluginManager {
	return &PluginManager{running: make(map[string]runGuard)}
}

func TestAcquireRunRejectsSecondRun(t *testing.T) {
	p := newTestPluginManager()

	if _, ok := p.acquireRun("kizuna_backup_lite"); !ok {
		t.Fatal("first acquireRun should succeed")
	}
	if _, ok := p.acquireRun("kizuna_backup_lite"); ok {
		t.Fatal("second acquireRun should be rejected while running")
	}
}

func TestAcquireRunReturnsUniqueRequestID(t *testing.T) {
	p := newTestPluginManager()

	id1, ok := p.acquireRun("kizuna_backup_lite")
	if !ok || id1 == "" {
		t.Fatalf("acquireRun should return a non-empty request_id (got %q, ok=%v)", id1, ok)
	}
	p.releaseRun("kizuna_backup_lite", id1)
	id2, ok := p.acquireRun("kizuna_backup_lite")
	if !ok {
		t.Fatal("second acquireRun after release should succeed")
	}
	if id1 == id2 {
		t.Fatalf("request_id must differ between runs: %q == %q", id1, id2)
	}
}

func TestReleaseRunAllowsNextRun(t *testing.T) {
	p := newTestPluginManager()

	rid, _ := p.acquireRun("kizuna_backup_lite")
	p.releaseRun("kizuna_backup_lite", rid)
	if _, ok := p.acquireRun("kizuna_backup_lite"); !ok {
		t.Fatal("acquireRun should succeed after releaseRun")
	}
}

func TestAcquireRunAllowsRetryAfterStale(t *testing.T) {
	p := newTestPluginManager()

	// Simulate a run whose result never arrived long ago.
	p.running["kizuna_backup_lite"] = runGuard{
		requestID: "stale",
		startedAt: time.Now().Add(-runGuardTimeout - time.Minute),
	}
	if _, ok := p.acquireRun("kizuna_backup_lite"); !ok {
		t.Fatal("acquireRun should allow a retry after the guard times out")
	}
}

func TestHandleAgentEventReleasesRun(t *testing.T) {
	p := newTestPluginManager()
	rid, _ := p.acquireRun("kizuna_backup_lite")

	p.HandleAgentEvent([]byte(`{"event":"backup_result","plugin":"kizuna_backup_lite","request_id":"` + rid + `","status":"success"}`))

	if _, ok := p.acquireRun("kizuna_backup_lite"); !ok {
		t.Fatal("acquireRun should succeed after the matching backup_result released the guard")
	}
}

// Regression: a scheduled run's result carries no request_id and must NOT
// release a manual run's guard, otherwise a second manual run could start while
// the first is still executing.
func TestHandleAgentEventIgnoresResultWithoutRequestID(t *testing.T) {
	p := newTestPluginManager()
	rid, _ := p.acquireRun("kizuna_backup_lite")

	p.HandleAgentEvent([]byte(`{"event":"backup_result","plugin":"kizuna_backup_lite","status":"success"}`))
	if _, ok := p.acquireRun("kizuna_backup_lite"); ok {
		t.Fatal("a result without request_id must not release the guard")
	}

	// The matching result does release it.
	p.HandleAgentEvent([]byte(`{"event":"backup_result","plugin":"kizuna_backup_lite","request_id":"` + rid + `"}`))
	if _, ok := p.acquireRun("kizuna_backup_lite"); !ok {
		t.Fatal("the matching request_id should release the guard")
	}
}

// Regression: a result for a different request must not release the guard.
func TestHandleAgentEventIgnoresMismatchedRequestID(t *testing.T) {
	p := newTestPluginManager()
	p.acquireRun("kizuna_backup_lite")

	p.HandleAgentEvent([]byte(`{"event":"backup_result","plugin":"kizuna_backup_lite","request_id":"someone-else"}`))

	if _, ok := p.acquireRun("kizuna_backup_lite"); ok {
		t.Fatal("a mismatched request_id must not release the guard")
	}
}

func TestHandleAgentEventIgnoresOtherEvents(t *testing.T) {
	p := newTestPluginManager()
	rid, _ := p.acquireRun("kizuna_backup_lite")

	// A different event or plugin must not release the guard.
	p.HandleAgentEvent([]byte(`{"event":"status"}`))
	p.HandleAgentEvent([]byte(`{"event":"backup_result","plugin":"other_plugin","request_id":"` + rid + `"}`))

	if _, ok := p.acquireRun("kizuna_backup_lite"); ok {
		t.Fatal("guard should still be held for kizuna_backup_lite")
	}
}
