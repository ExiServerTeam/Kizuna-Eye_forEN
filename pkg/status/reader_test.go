package status

import "testing"

// Regression: Clone returns a deep copy.
func TestCloneIsDeepCopy(t *testing.T) {
	orig := &SystemStatus{
		CPUPerCore:   []float64{1, 2, 3},
		LoadAverage:  []float64{0.1, 0.2, 0.3},
		Disks:        []DiskInfo{{Path: "/", Total: 100}},
		Processes:    []ProcessInfo{{PID: 1, Name: "init"}},
		Network:      &NetworkIO{Up: 10, Down: 20},
		NetworkSpeed: &NetworkSpeed{Up: 1, Down: 2},
	}

	clone := orig.Clone()
	if clone == orig {
		t.Fatal("Clone returned the same pointer")
	}

	// Mutating the original must not affect the clone.
	orig.CPUPerCore[0] = 999
	orig.LoadAverage[0] = 9.9
	orig.Disks[0].Path = "/changed"
	orig.Processes[0].Name = "changed"
	orig.Network.Up = 999
	orig.NetworkSpeed.Down = 999

	if clone.CPUPerCore[0] != 1 {
		t.Errorf("CPUPerCore shared: got %v", clone.CPUPerCore[0])
	}
	if clone.LoadAverage[0] != 0.1 {
		t.Errorf("LoadAverage shared: got %v", clone.LoadAverage[0])
	}
	if clone.Disks[0].Path != "/" {
		t.Errorf("Disks shared: got %v", clone.Disks[0].Path)
	}
	if clone.Processes[0].Name != "init" {
		t.Errorf("Processes shared: got %v", clone.Processes[0].Name)
	}
	if clone.Network.Up != 10 {
		t.Errorf("Network shared: got %v", clone.Network.Up)
	}
	if clone.NetworkSpeed.Down != 2 {
		t.Errorf("NetworkSpeed shared: got %v", clone.NetworkSpeed.Down)
	}
}

func TestCloneNil(t *testing.T) {
	var s *SystemStatus
	if s.Clone() != nil {
		t.Fatal("nil.Clone() should return nil")
	}
}
