package status

import "testing"

// SanitizeForViewer must remove the process list and per-disk S.M.A.R.T
// metadata while keeping the usage fields the public viewer is allowed to see.
func TestSanitizeForViewer(t *testing.T) {
	s := &SystemStatus{
		Timestamp:   1,
		CPUUsage:    10,
		MemPercent:  20,
		DiskPercent: 30,
		Disks: []DiskInfo{
			{
				Path: "/", Total: 100, Used: 40, Percent: 40, Temp: 35, Health: "PASSED",
				Model: "Samsung SSD", Serial: "S123", WriteBytes: 999, PowerOnHours: 42, RotationRate: 7200,
			},
		},
		Processes: []ProcessInfo{{PID: 1, Name: "init", CPU: 1, MemMB: 2, User: "root"}},
	}

	out := s.SanitizeForViewer()
	if out == nil {
		t.Fatal("SanitizeForViewer returned nil for a non-nil status")
	}
	if out.Processes != nil {
		t.Errorf("processes must be removed, got %#v", out.Processes)
	}
	if len(out.Disks) != 1 {
		t.Fatalf("disk count changed: %d", len(out.Disks))
	}
	d := out.Disks[0]
	if d.Model != "" || d.Serial != "" || d.WriteBytes != 0 || d.PowerOnHours != 0 || d.RotationRate != 0 {
		t.Errorf("S.M.A.R.T metadata not stripped: %#v", d)
	}
	if d.Health != "" || d.Temp != 0 {
		t.Errorf("health/temp must be stripped for public viewers: %#v", d)
	}
	if d.Path != "/" || d.Total != 100 || d.Used != 40 || d.Percent != 40 {
		t.Errorf("usage fields must be preserved: %#v", d)
	}

	// The original must not be mutated (deep copy).
	if len(s.Processes) != 1 || s.Disks[0].Serial != "S123" {
		t.Error("SanitizeForViewer mutated the source status")
	}
}

func TestSanitizeForViewerNil(t *testing.T) {
	var s *SystemStatus
	if s.SanitizeForViewer() != nil {
		t.Fatal("nil.SanitizeForViewer() should return nil")
	}
}
