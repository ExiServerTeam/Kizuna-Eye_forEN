package status

// ============================================================
// NetworkIO はネットワークI/Oの累積バイト数
// ============================================================
type NetworkIO struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

// ============================================================
// NetworkSpeed はネットワーク速度（bytes/sec）
// ============================================================
type NetworkSpeed struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

// ============================================================
// DiskInfo は個別のディスク情報
// ============================================================
type DiskInfo struct {
	Path    string  `json:"path"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Percent float64 `json:"percent"`
	Temp    float64 `json:"temp,omitempty"`
	Health  string  `json:"health,omitempty"`
}

// ============================================================
// ProcessInfo は個別プロセスの情報（Top N 用）
// ============================================================
type ProcessInfo struct {
	PID   int32   `json:"pid"`
	Name  string  `json:"name"`
	CPU   float64 `json:"cpu"`            // %
	MemMB float64 `json:"mem_mb"`         // MB
	User  string  `json:"user,omitempty"` // 実行ユーザー
}

// ============================================================
// SystemStatus はシステム全体の状態
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

	// ★ Phase 6: Top N プロセス（CPU降順）
	Processes []ProcessInfo `json:"processes,omitempty"`

	// バックアップ情報
	Backup struct {
		LastRun string `json:"last_run,omitempty"`
		Status  string `json:"status,omitempty"`
		Size    int64  `json:"size,omitempty"`
	} `json:"backup,omitempty"`
}
