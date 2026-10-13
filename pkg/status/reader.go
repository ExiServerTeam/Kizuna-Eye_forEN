package status

import (
	"encoding/json"
	"fmt"
	"io"
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

// Validate performs simple range checks on the SystemStatus.
func (s *SystemStatus) Validate() error {
	if s == nil {
		return fmt.Errorf("ステータスが nil です")
	}
	if s.Timestamp < 0 {
		return fmt.Errorf("timestamp が不正です: %d", s.Timestamp)
	}
	if s.CPUUsage < 0 || s.CPUUsage > 100 {
		return fmt.Errorf("CPUUsage が範囲外です: %.2f", s.CPUUsage)
	}
	if s.MemPercent < 0 || s.MemPercent > 100 {
		return fmt.Errorf("MemPercent が範囲外です: %.2f", s.MemPercent)
	}
	if s.DiskPercent < 0 || s.DiskPercent > 100 {
		return fmt.Errorf("DiskPercent が範囲外です: %.2f", s.DiskPercent)
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
