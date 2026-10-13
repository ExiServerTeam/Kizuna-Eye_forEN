package status

// ============================================================
// NetworkIO holds cumulative network I/O bytes.
// ============================================================
type NetworkIO struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

// ============================================================
// NetworkSpeed is network speed in bytes/sec.
// ============================================================
type NetworkSpeed struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

// ============================================================
// DiskInfo is per-disk information.
// ============================================================
type DiskInfo struct {
	Path    string  `json:"path"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Percent float64 `json:"percent"`
	Temp    float64 `json:"temp,omitempty"`
	Health  string  `json:"health,omitempty"`

	// Optional S.M.A.R.T metadata (present only when smartctl is
	// available and the device reports it).
	Model        string `json:"model,omitempty"`
	Serial       string `json:"serial,omitempty"`
	WriteBytes   uint64 `json:"write_bytes,omitempty"`   // total bytes written over the device lifetime
	PowerOnHours uint64 `json:"power_on_hours,omitempty"`
	RotationRate int    `json:"rotation_rate,omitempty"`   // RPM; 0 (SSD) and -1 (unknown) are omitted
}

// ============================================================
// ProcessInfo is per-process information (Top N).
// ============================================================
type ProcessInfo struct {
	PID   int32   `json:"pid"`
	Name  string  `json:"name"`
	CPU   float64 `json:"cpu"`            // % per-process CPU usage (system-wide is SystemStatus.CPUUsage="cpu_usage")
	MemMB float64 `json:"mem_mb"`         // MB
	User  string  `json:"user,omitempty"` // owning user
}

// ============================================================
// SystemStatus is the overall system state.
// ============================================================
type SystemStatus struct {
	Timestamp     int64      `json:"timestamp"`
	Uptime        uint64     `json:"uptime"`
	CPUUsage      float64    `json:"cpu_usage"`
	CPUPerCore    []float64  `json:"cpu_per_core,omitempty"`
	MemTotal      uint64     `json:"mem_total"`
	MemUsed       uint64     `json:"mem_used"`
	MemPercent    float64    `json:"mem_percent"`
	DiskTotal     uint64     `json:"disk_total"`
	DiskUsed      uint64     `json:"disk_used"`
	DiskPercent   float64    `json:"disk_percent"`
	Disks         []DiskInfo `json:"disks,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	Fallback      bool       `json:"fallback,omitempty"`
	CPUErrorCount int        `json:"cpu_error_count,omitempty"`

	// Phase 3
	CPUModel    string     `json:"cpu_model,omitempty"`
	CPUTemp     float64    `json:"cpu_temp,omitempty"`
	LoadAverage []float64  `json:"load_average,omitempty"`
	Network     *NetworkIO `json:"network,omitempty"`

	// Phase 4
	NetworkSpeed *NetworkSpeed `json:"network_speed,omitempty"`

	// Top N processes (CPU descending)
	Processes []ProcessInfo `json:"processes,omitempty"`

	// Backup info
	Backup struct {
		Name    string `json:"name,omitempty"`
		LastRun string `json:"last_run,omitempty"`
		NextRun string `json:"next_run,omitempty"`
		Status  string `json:"status,omitempty"`
		Size    int64  `json:"size,omitempty"`
	} `json:"backup,omitempty"`
}
