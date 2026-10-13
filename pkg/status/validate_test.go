package status

import (
	"math"
	"testing"
)

// NaN/Inf must be rejected (a naive range check would let them through).
func TestValidateRejectsNaNInf(t *testing.T) {
	cases := []struct {
		name string
		mut  func(s *SystemStatus)
	}{
		{"cpu NaN", func(s *SystemStatus) { s.CPUUsage = math.NaN() }},
		{"cpu +Inf", func(s *SystemStatus) { s.CPUUsage = math.Inf(1) }},
		{"mem NaN", func(s *SystemStatus) { s.MemPercent = math.NaN() }},
		{"mem -Inf", func(s *SystemStatus) { s.MemPercent = math.Inf(-1) }},
		{"disk NaN", func(s *SystemStatus) { s.DiskPercent = math.NaN() }},
		{"disk +Inf", func(s *SystemStatus) { s.DiskPercent = math.Inf(1) }},
	}
	for _, c := range cases {
		s := &SystemStatus{Timestamp: 1}
		c.mut(s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", c.name)
		}
	}
}

// A normal status still passes.
func TestValidateAcceptsNormal(t *testing.T) {
	s := &SystemStatus{Timestamp: 1, CPUUsage: 50, MemPercent: 60, DiskPercent: 70}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// Out-of-range values are rejected.
func TestValidateRejectsOutOfRange(t *testing.T) {
	for _, v := range []float64{-1, 101} {
		if err := (&SystemStatus{Timestamp: 1, CPUUsage: v}).Validate(); err == nil {
			t.Errorf("CPUUsage %v: want error", v)
		}
	}
}
