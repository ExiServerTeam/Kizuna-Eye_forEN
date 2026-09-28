package main

import (
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
