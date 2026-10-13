package status

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

// ReadFromFile reads a SystemStatus from a file.
// Returns (nil, nil) if the file does not exist.
func ReadFromFile(path string) (*SystemStatus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // missing file is not an error
		}
		return nil, fmt.Errorf("ファイル読み取りエラー: %w", err)
	}

	return ReadFromJSON(data)
}

// ReadFromJSON reads a SystemStatus from JSON bytes.
func ReadFromJSON(data []byte) (*SystemStatus, error) {
	var s SystemStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("JSONパースエラー: %w", err)
	}
	return &s, nil
}

// ReadFromReader reads a SystemStatus from an io.Reader.
func ReadFromReader(r io.Reader) (*SystemStatus, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("読み取りエラー: %w", err)
	}
	return ReadFromJSON(data)
}

// ReadFromFileWithDefault reads a file, returning the default on failure.
// Missing files and parse errors also return the default.
func ReadFromFileWithDefault(path string, defaultStatus *SystemStatus) (*SystemStatus, error) {
	s, err := ReadFromFile(path)
	if err != nil {
		return defaultStatus, err
	}
	if s == nil {
		return defaultStatus, nil
	}
	return s, nil
}

// Validate performs range checks on the SystemStatus before it is stored,
// broadcast, or exported as Prometheus metrics.
//
// A status with an unusable value must be rejected wholesale: the dashboard
// treats it as the latest state, appends it to the metrics history, and feeds
// it to the alert engine, so one poisoned field would corrupt all three. The
// checks cover every numeric field a client can render.
func (s *SystemStatus) Validate() error {
	if s == nil {
		return fmt.Errorf("ステータスが nil です")
	}
	// A non-positive timestamp is not a usable sample (the dashboard uses
	// "timestamp > 0" to tell a real status from arbitrary JSON), and a
	// negative one would place the sample before the epoch.
	if s.Timestamp <= 0 {
		return fmt.Errorf("timestamp が不正です: %d", s.Timestamp)
	}

	// NaN/Inf would pass a naive range check (all comparisons are false).
	// Reject them explicitly so a buggy/hostile agent cannot poison metrics.
	for _, f := range []struct {
		name string
		val  float64
	}{
		{"CPUUsage", s.CPUUsage},
		{"MemPercent", s.MemPercent},
		{"DiskPercent", s.DiskPercent},
	} {
		if math.IsNaN(f.val) || math.IsInf(f.val, 0) {
			return fmt.Errorf("%s が NaN/Inf です", f.name)
		}
		if f.val < 0 || f.val > 100 {
			return fmt.Errorf("%s が範囲外です: %.2f", f.name, f.val)
		}
	}

	// Per-core CPU.
	for i, v := range s.CPUPerCore {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
			return fmt.Errorf("CPUPerCore[%d] が不正です: %.2f", i, v)
		}
	}
	// Load average (non-negative, finite).
	for i, v := range s.LoadAverage {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return fmt.Errorf("LoadAverage[%d] が不正です: %.2f", i, v)
		}
	}
	// CPU temperature: 0 means "sensor unavailable"; otherwise a plausible
	// range (the alert threshold validator uses the same 0..150 bound).
	if math.IsNaN(s.CPUTemp) || math.IsInf(s.CPUTemp, 0) || s.CPUTemp < 0 || s.CPUTemp > 150 {
		return fmt.Errorf("CPUTemp が不正です: %.1f", s.CPUTemp)
	}
	// Per-disk usage and S.M.A.R.T values.
	for i, d := range s.Disks {
		if math.IsNaN(d.Percent) || math.IsInf(d.Percent, 0) || d.Percent < 0 || d.Percent > 100 {
			return fmt.Errorf("Disks[%d].Percent が不正です: %.2f", i, d.Percent)
		}
		if math.IsNaN(d.Temp) || math.IsInf(d.Temp, 0) || d.Temp < 0 || d.Temp > 150 {
			return fmt.Errorf("Disks[%d].Temp が不正です: %.1f", i, d.Temp)
		}
	}
	// Processes (rendered in the Top-N table).
	for i, p := range s.Processes {
		if p.PID < 0 {
			return fmt.Errorf("Processes[%d].PID が不正です: %d", i, p.PID)
		}
		if math.IsNaN(p.CPU) || math.IsInf(p.CPU, 0) || p.CPU < 0 {
			return fmt.Errorf("Processes[%d].CPU が不正です: %.2f", i, p.CPU)
		}
		if math.IsNaN(p.MemMB) || math.IsInf(p.MemMB, 0) || p.MemMB < 0 {
			return fmt.Errorf("Processes[%d].MemMB が不正です: %.2f", i, p.MemMB)
		}
	}
	if s.CPUErrorCount < 0 {
		return fmt.Errorf("CPUErrorCount が不正です: %d", s.CPUErrorCount)
	}
	return nil
}

// IsStale reports whether the status is older than maxAge seconds.
func (s *SystemStatus) IsStale(maxAge int64) bool {
	if s == nil {
		return true
	}
	now := time.Now().Unix()
	return (now - s.Timestamp) > maxAge
}

// SanitizeForViewer returns a copy of the status with fields that are not
// part of the public viewer view removed: the live process list (PIDs, users)
// and the S.M.A.R.T metadata of each disk (model, serial, written bytes,
// power-on hours, rotation rate). Public viewer mode exposes only CPU/memory/
// disk *usage*, so these must not be served to unauthenticated clients even
// though the UI hides the detail modals.
func (s *SystemStatus) SanitizeForViewer() *SystemStatus {
	if s == nil {
		return nil
	}
	c := s.Clone()
	c.Processes = nil
	for i := range c.Disks {
		c.Disks[i].Model = ""
		c.Disks[i].Serial = ""
		c.Disks[i].WriteBytes = 0
		c.Disks[i].PowerOnHours = 0
		c.Disks[i].RotationRate = 0
		// Health and temperature are also S.M.A.R.T-derived, so they are
		// not part of the "usage only" public view either.
		c.Disks[i].Health = ""
		c.Disks[i].Temp = 0
	}
	return c
}

// Clone returns a deep copy of the SystemStatus.
// Slices and pointer fields are copied, not shared.
func (s *SystemStatus) Clone() *SystemStatus {
	if s == nil {
		return nil
	}
	copied := *s

	if s.CPUPerCore != nil {
		copied.CPUPerCore = append([]float64(nil), s.CPUPerCore...)
	}
	if s.LoadAverage != nil {
		copied.LoadAverage = append([]float64(nil), s.LoadAverage...)
	}
	if s.Disks != nil {
		copied.Disks = append([]DiskInfo(nil), s.Disks...)
	}
	if s.Processes != nil {
		copied.Processes = append([]ProcessInfo(nil), s.Processes...)
	}
	if s.Network != nil {
		n := *s.Network
		copied.Network = &n
	}
	if s.NetworkSpeed != nil {
		ns := *s.NetworkSpeed
		copied.NetworkSpeed = &ns
	}
	return &copied
}
