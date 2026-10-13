package main

import (
	"encoding/json"
	"testing"

	"Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/status"
)

func TestAcceptAgentPayload(t *testing.T) {
	cases := []struct {
		name                       string
		isAgent, isStatus, canProm bool
		wantAccept, wantPromote    bool
	}{
		{"authenticated agent status", true, true, false, true, false},
		{"authenticated agent non-status", true, false, false, true, false},
		{"legacy promotion of non-browser agent", false, true, true, true, true},
		// Regression: a browser (no promotion eligibility) must not be able to
		// store a spoofed status via SetLastStatus.
		{"browser status spoof blocked", false, true, false, false, false},
		{"non-agent non-status blocked", false, false, false, false, false},
		{"non-status never promotes", false, false, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			accept, promote := acceptAgentPayload(c.isAgent, c.isStatus, c.canProm)
			if accept != c.wantAccept || promote != c.wantPromote {
				t.Fatalf("acceptAgentPayload(%v,%v,%v) = (%v,%v), want (%v,%v)",
					c.isAgent, c.isStatus, c.canProm, accept, promote, c.wantAccept, c.wantPromote)
			}
		})
	}
}

func TestEligibleForHeuristicPromotion(t *testing.T) {
	cases := []struct {
		name        string
		agentToken  string
		isAgentRole bool
		origin      string
		want        bool
	}{
		{"tokenless non-browser agent is eligible", "", true, "", true},
		// Regression: a browser always sends Origin, so it must never be
		// eligible for tokenless agent promotion.
		{"browser with Origin is not eligible", "", true, "http://evil.example", false},
		{"token configured disables the heuristic", "tok", true, "", false},
		{"missing role=agent is not eligible", "", false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := eligibleForHeuristicPromotion(c.agentToken, c.isAgentRole, c.origin); got != c.want {
				t.Fatalf("eligibleForHeuristicPromotion(%q,%v,%q) = %v, want %v",
					c.agentToken, c.isAgentRole, c.origin, got, c.want)
			}
		})
	}
}

// An out-of-range status (e.g. cpu_usage=1e9) must not be stored, so it
// cannot pollute metrics, history, or Prometheus output.
func TestHubSetLastStatusRejectsOutOfRange(t *testing.T) {
	hub := NewHub(10, logger.NewLogger(&logger.Options{Level: logger.INFO}))

	// First a valid status is accepted.
	hub.SetLastStatus(&status.SystemStatus{Timestamp: 1, CPUUsage: 10, MemPercent: 20, DiskPercent: 30})
	if hub.GetLastStatus() == nil {
		t.Fatal("valid status should be stored")
	}

	// An out-of-range status is dropped, leaving the previous one intact.
	hub.SetLastStatus(&status.SystemStatus{Timestamp: 2, CPUUsage: 1e9, MemPercent: 20, DiskPercent: 30})
	got := hub.GetLastStatus()
	if got == nil || got.CPUUsage == 1e9 {
		t.Fatalf("out-of-range status must be rejected, got %#v", got)
	}
	if got.Timestamp != 1 {
		t.Fatalf("previous status should remain, got timestamp %d", got.Timestamp)
	}

	// Negative percentages are also rejected.
	hub.SetLastStatus(&status.SystemStatus{Timestamp: 3, CPUUsage: 10, MemPercent: -5, DiskPercent: 30})
	if got := hub.GetLastStatus(); got == nil || got.MemPercent < 0 {
		t.Fatalf("negative mem_percent must be rejected, got %#v", got)
	}
}

func TestHubSetLastStatusIgnoresNil(t *testing.T) {
	hub := NewHub(10, logger.NewLogger(&logger.Options{Level: logger.INFO}))

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SetLastStatus(nil) should not panic: %v", r)
		}
	}()

	hub.SetLastStatus(nil)
	if got := hub.GetLastStatus(); got != nil {
		t.Fatalf("expected nil status after SetLastStatus(nil), got %#v", got)
	}

	statusSample := &status.SystemStatus{DiskTotal: 100, DiskUsed: 20}
	hub.SetLastStatus(statusSample)
	if got := hub.GetLastStatus(); got == nil || got.DiskTotal != 100 || got.DiskUsed != 20 {
		t.Fatalf("expected status to be stored, got %#v", got)
	}
}

// Regression: a public-viewer broadcast must not expose the process list or
// disk S.M.A.R.T metadata (model/serial/health/temp), and non-status messages
// (events) must pass through unchanged.
func TestSanitizeStatusJSON(t *testing.T) {
	statusMsg := []byte(`{"timestamp":1,"cpu_usage":10,"processes":[{"pid":1,"name":"init","user":"root"}],"disks":[{"path":"/","percent":40,"model":"SSD","serial":"S1","health":"PASSED","temp":35}]}`)
	out := sanitizeStatusJSON(statusMsg)
	var s status.SystemStatus
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatalf("sanitized output is not valid JSON: %v", err)
	}
	if s.Processes != nil {
		t.Errorf("processes must be stripped: %#v", s.Processes)
	}
	if len(s.Disks) != 1 || s.Disks[0].Model != "" || s.Disks[0].Serial != "" || s.Disks[0].Health != "" || s.Disks[0].Temp != 0 {
		t.Errorf("disk S.M.A.R.T metadata must be stripped: %#v", s.Disks)
	}
	if s.CPUUsage != 10 || s.Disks[0].Percent != 40 {
		t.Errorf("usage fields must be preserved: %#v", s)
	}

	// A non-status message (event) must pass through unchanged.
	eventMsg := []byte(`{"event":"backup_result","plugin":"p","status":"success"}`)
	if got := sanitizeStatusJSON(eventMsg); string(got) != string(eventMsg) {
		t.Errorf("event message must be unchanged, got %s", got)
	}
}
