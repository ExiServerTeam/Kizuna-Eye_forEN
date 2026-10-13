package module

import (
	"testing"

	"Kizuna-Eye/pkg/status"
)

// Regression: GetStatus must return a deep copy. A shallow copy shares the
// backing arrays of Disks/Processes/CPUPerCore/etc., so a caller that
// mutates the returned status would corrupt the module's live status.
func TestGetStatusReturnsDeepCopy(t *testing.T) {
	m := NewSystemModule(nil)
	m.status = &status.SystemStatus{
		CPUPerCore:  []float64{1, 2, 3},
		LoadAverage: []float64{0.1, 0.2, 0.3},
		Disks: []status.DiskInfo{
			{Path: "/", Percent: 50},
		},
		Processes: []status.ProcessInfo{
			{PID: 1, Name: "init"},
		},
		Network:      &status.NetworkIO{Up: 10, Down: 20},
		NetworkSpeed: &status.NetworkSpeed{Up: 1, Down: 2},
	}

	got := m.GetStatus()
	if got == nil {
		t.Fatal("GetStatus returned nil")
	}

	// Mutate every slice/pointer field of the returned copy.
	got.CPUPerCore[0] = 99
	got.LoadAverage[0] = 99
	got.Disks[0].Path = "/mutated"
	got.Processes[0].Name = "mutated"
	got.Network.Up = 999
	got.NetworkSpeed.Down = 999

	stored := m.GetStatus()
	if stored.CPUPerCore[0] != 1 {
		t.Errorf("CPUPerCore mutated: got %v, want 1", stored.CPUPerCore[0])
	}
	if stored.LoadAverage[0] != 0.1 {
		t.Errorf("LoadAverage mutated: got %v, want 0.1", stored.LoadAverage[0])
	}
	if stored.Disks[0].Path != "/" {
		t.Errorf("Disks mutated: got %q, want \"/\"", stored.Disks[0].Path)
	}
	if stored.Processes[0].Name != "init" {
		t.Errorf("Processes mutated: got %q, want \"init\"", stored.Processes[0].Name)
	}
	if stored.Network.Up != 10 {
		t.Errorf("Network mutated: got %d, want 10", stored.Network.Up)
	}
	if stored.NetworkSpeed.Down != 2 {
		t.Errorf("NetworkSpeed mutated: got %d, want 2", stored.NetworkSpeed.Down)
	}
}
