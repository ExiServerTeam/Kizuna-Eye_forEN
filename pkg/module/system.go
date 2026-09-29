package module

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"

	"Kizuna-Eye/pkg/status"
)

// SystemConfig is the system module configuration.
type SystemConfig struct {
	DiskPath          string `json:"disk_path"`
	Interval          int    `json:"interval"`
	HealthInterval    int    `json:"health_interval"`
	StaleThreshold    int    `json:"stale_threshold"`
	ShutdownTimeout   int    `json:"shutdown_timeout"`
	CPUErrorThreshold int    `json:"cpu_error_threshold"`
}

// ============================================================
// smartCache caches S.M.A.R.T data.
// ============================================================
type smartCache struct {
	mu        sync.RWMutex
	entries   map[string]smartEntry
	interval  time.Duration
	lastCheck time.Time
	available bool
	checked   bool
}

// smartEntry is the cached S.M.A.R.T data for one block device.
// Zero values mean "not reported" and are omitted from the JSON.
type smartEntry struct {
	temp         float64
	health       string
	model        string
	serial       string
	writeBytes   uint64
	powerOnHours uint64
	rotationRate int
}

func newSmartCache() *smartCache {
	return &smartCache{
		entries:  make(map[string]smartEntry),
		interval: 60 * time.Second,
	}
}

func (s *smartCache) shouldRefresh() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.Since(s.lastCheck) >= s.interval
}

// get returns the cached S.M.A.R.T data for a device. The second return
// value reports whether an entry exists at all, so callers can distinguish
// "device not checked yet" from "checked but no data".
func (s *smartCache) get(device string) (smartEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[device]
	return e, ok
}

func (s *smartCache) set(device string, e smartEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[device] = e
}

func (s *smartCache) markChecked() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCheck = time.Now()
}

func (s *smartCache) checkAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checked {
		return s.available
	}
	s.checked = true
	if _, err := exec.LookPath("smartctl"); err != nil {
		s.available = false
		return false
	}
	s.available = true
	return true
}

// ============================================================
// ★ Phase 6: processCache
//
// processCache caches Top N processes.
//   - Refreshes every 3s (calling every second is expensive)
//   - CPU% is computed from the difference since the last sample
//
// ============================================================
type processCache struct {
	mu         sync.Mutex
	list       []status.ProcessInfo
	lastUpdate time.Time
	interval   time.Duration
	topN       int

	// For CPU% delta calculation
	lastCPUTimes   map[int32]float64 // PID -> total CPU seconds
	lastSampleTime time.Time
}

func newProcessCache(topN int) *processCache {
	if topN < 1 {
		topN = 10
	}
	return &processCache{
		interval:     3 * time.Second,
		topN:         topN,
		lastCPUTimes: make(map[int32]float64),
	}
}

func (c *processCache) get() []status.ProcessInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.list != nil && time.Since(c.lastUpdate) < c.interval {
		return c.list
	}

	c.list = c.collect()
	c.lastUpdate = time.Now()
	return c.list
}

func (c *processCache) collect() []status.ProcessInfo {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}

	now := time.Now()
	elapsed := now.Sub(c.lastSampleTime).Seconds()
	hasBaseline := !c.lastSampleTime.IsZero() && elapsed > 0

	newTimes := make(map[int32]float64, len(procs))
	list := make([]status.ProcessInfo, 0, len(procs))

	for _, p := range procs {
		name, err := p.Name()
		if err != nil || name == "" {
			continue
		}

		var cpuPct float64
		if t, err := p.Times(); err == nil && t != nil {
			totalSec := t.User + t.System
			newTimes[p.Pid] = totalSec

			if hasBaseline {
				if prev, ok := c.lastCPUTimes[p.Pid]; ok {
					delta := totalSec - prev
					if delta > 0 {
						cpuPct = (delta / elapsed) * 100
						if cpuPct > 100 {
							cpuPct = 100
						}
					}
				}
			}
		}

		var memMB float64
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			memMB = float64(mi.RSS) / 1024 / 1024
		}

		user, _ := p.Username()

		list = append(list, status.ProcessInfo{
			PID:   p.Pid,
			Name:  name,
			CPU:   cpuPct,
			MemMB: memMB,
			User:  user,
		})
	}

	c.lastCPUTimes = newTimes
	c.lastSampleTime = now

	sort.Slice(list, func(i, j int) bool {
		return list[i].CPU > list[j].CPU
	})

	if len(list) > c.topN {
		list = list[:c.topN]
	}
	return list
}

// ============================================================
// SystemModule
// ============================================================
type SystemModule struct {
	config     *SystemConfig
	logger     Logger
	mu         sync.RWMutex
	status     *status.SystemStatus
	lastError  string
	isFallback bool

	cpuModel string

	// Phase 4
	prevNetUp   uint64
	prevNetDown uint64
	prevNetTime time.Time
	hasPrevNet  bool

	smart *smartCache

	// ★ Phase 6
	processes *processCache

	cpuCollector *cpuCollector
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// ---------- CPU collector ----------
type cpuCollector struct {
	mu          sync.RWMutex
	cache       float64
	err         error
	errCount    int
	threshold   int
	samples     []float64
	sampleSize  int
	initialized bool
}

func newCPUCollector(threshold int) *cpuCollector {
	if threshold < 1 {
		threshold = 3
	}
	return &cpuCollector{
		threshold:   threshold,
		sampleSize:  threshold,
		samples:     make([]float64, 0, threshold),
		initialized: false,
	}
}

func (c *cpuCollector) update(val float64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.errCount++
		c.err = err
		return
	}
	c.errCount = 0
	c.err = nil
	if !c.initialized {
		c.cache = val
		c.initialized = true
		return
	}
	c.samples = append(c.samples, val)
	if len(c.samples) > c.sampleSize {
		c.samples = c.samples[1:]
	}
	if len(c.samples) < c.sampleSize {
		return
	}
	var sum float64
	for _, v := range c.samples {
		sum += v
	}
	c.cache = sum / float64(len(c.samples))
}

func (c *cpuCollector) get() (float64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.errCount >= c.threshold {
		return c.cache, fmt.Errorf("CPU取得失敗 (連続%d回): %v", c.errCount, c.err)
	}
	return c.cache, nil
}

func (c *cpuCollector) getErrorCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.errCount
}

// ---------- Constructor ----------
func NewSystemModule(logger Logger) *SystemModule {
	return &SystemModule{
		logger: logger,
		config: &SystemConfig{
			DiskPath:          "/",
			Interval:          1,
			HealthInterval:    0,
			StaleThreshold:    0,
			ShutdownTimeout:   5,
			CPUErrorThreshold: 3,
		},
		smart:     newSmartCache(),
		processes: newProcessCache(10),
	}
}

// ---------- Module interface ----------
func (m *SystemModule) Name() string { return "system" }

func (m *SystemModule) Interval() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.config.Interval > 0 {
		return time.Duration(m.config.Interval) * time.Second
	}
	return 1 * time.Second
}

func (m *SystemModule) Description() string { return "システム情報収集モジュール" }

// ---------- Init ----------
func (m *SystemModule) Init(ctx context.Context) error {
	if m.logger != nil {
		m.logger.Info("システムモジュール初期化: DiskPath=%s, Interval=%d秒", m.config.DiskPath, m.config.Interval)
	}

	m.mu.Lock()
	if _, err := os.Stat(m.config.DiskPath); err != nil {
		if m.logger != nil {
			m.logger.Warn("ディスクパス '%s' が存在しません → '/' にフォールバック", m.config.DiskPath)
		}
		m.config.DiskPath = "/"
		m.isFallback = true
	}
	m.mu.Unlock()

	m.cacheCPUModel()

	if m.smart.checkAvailable() {
		if m.logger != nil {
			m.logger.Info("smartctl 検出: ディスクS.M.A.R.T 情報を取得します")
		}
	} else {
		if m.logger != nil {
			m.logger.Info("smartctl 未検出: ディスク温度・健康状態は取得しません")
		}
	}

	m.cpuCollector = newCPUCollector(m.config.CPUErrorThreshold)
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.wg.Add(1)
	go m.asyncCPUCollector(ctx)

	m.updateStatus()
	return nil
}

func (m *SystemModule) cacheCPUModel() {
	info, err := cpu.Info()
	if err != nil || len(info) == 0 {
		if m.logger != nil {
			m.logger.Debug("CPU型番取得失敗: %v", err)
		}
		return
	}
	model := strings.TrimSpace(info[0].ModelName)
	m.mu.Lock()
	m.cpuModel = model
	m.mu.Unlock()
	if m.logger != nil {
		m.logger.Info("CPU型番: %s", model)
	}
}

// ---------- Run ----------
func (m *SystemModule) Run(ctx context.Context) error {
	m.updateStatus()
	return nil
}

// ---------- Configure ----------
func (m *SystemModule) Configure(config interface{}) error {
	cfg, ok := config.(*SystemConfig)
	if !ok {
		return fmt.Errorf("設定型が不正です。*SystemConfig が必要です")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.DiskPath == "" {
		cfg.DiskPath = "/"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 1
	}
	if cfg.HealthInterval < 0 {
		cfg.HealthInterval = 0
	}
	if cfg.StaleThreshold < 0 {
		cfg.StaleThreshold = 0
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 5
	}
	if cfg.CPUErrorThreshold <= 0 {
		cfg.CPUErrorThreshold = 3
	}
	m.config = cfg
	return nil
}

// ---------- HealthCheckable ----------
func (m *SystemModule) Health(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.status == nil {
		return fmt.Errorf("ステータス未初期化")
	}
	if m.lastError != "" {
		return fmt.Errorf("システム情報収集エラー: %s", m.lastError)
	}
	threshold := m.getStaleThresholdLocked()
	elapsed := time.Since(time.Unix(m.status.Timestamp, 0))
	if elapsed > threshold {
		return fmt.Errorf("最終更新から %v 経過 (閾値: %v)", elapsed.Round(time.Second), threshold)
	}
	return nil
}

func (m *SystemModule) HealthInterval() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.config.HealthInterval > 0 {
		val := time.Duration(m.config.HealthInterval) * time.Second
		if val < 10*time.Second {
			return 10 * time.Second
		}
		maxVal := time.Duration(m.config.Interval*2) * time.Second
		if val > maxVal {
			return maxVal
		}
		return val
	}
	val := time.Duration(m.config.Interval*2) * time.Second
	if val < 10*time.Second {
		return 10 * time.Second
	}
	return val
}

// ---------- Shutdownable ----------
func (m *SystemModule) Shutdown(ctx context.Context) error {
	if m.logger != nil {
		m.logger.Info("システムモジュール Shutdown 開始")
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.RLock()
	timeout := time.Duration(m.config.ShutdownTimeout) * time.Second
	m.mu.RUnlock()
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		if m.logger != nil {
			m.logger.Info("システムモジュール Shutdown 完了")
		}
		return nil
	case <-timeoutCtx.Done():
		if m.logger != nil {
			m.logger.Warn("システムモジュール Shutdown タイムアウト (%v)", timeout)
		}
		return timeoutCtx.Err()
	}
}

// ---------- Internal methods ----------
func (m *SystemModule) GetStatus() *status.SystemStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.status == nil {
		return nil
	}
	c := *m.status
	return &c
}

func (m *SystemModule) getStaleThresholdLocked() time.Duration {
	if m.config.StaleThreshold > 0 {
		return time.Duration(m.config.StaleThreshold) * time.Second
	}
	return time.Duration(m.config.Interval*3) * time.Second
}

// ============================================================
// updateStatus collects system information.
// ============================================================
func (m *SystemModule) updateStatus() {
	m.mu.RLock()
	diskPath := m.config.DiskPath
	isFallback := m.isFallback
	cpuModel := m.cpuModel
	m.mu.RUnlock()

	// ---------- CPU ----------
	// gopsutil keeps separate baselines for overall (percpu=false) and
	// per-core (percpu=true) sampling, so these two calls do not race.
	cpuVal, cpuErr := m.cpuCollector.get()
	cpuErrCount := m.cpuCollector.getErrorCount()
	cpuPerCore, _ := cpu.Percent(0, true)
	cpuTemp := collectCPUTemp()

	// ---------- Memory ----------
	memInfo, memErr := mem.VirtualMemory()
	if memErr != nil {
		if m.logger != nil {
			m.logger.Warn("メモリ情報取得失敗: %v", memErr)
		}
		memInfo = &mem.VirtualMemoryStat{Total: 0, Used: 0, UsedPercent: 0}
	}

	// ---------- Disk ----------
	var disks []status.DiskInfo
	var totalDiskUsed, totalDiskTotal uint64
	var totalDiskPercent float64

	refreshSmart := m.smart.shouldRefresh()
	if refreshSmart {
		m.smart.markChecked()
	}

	partitions, err := disk.Partitions(false)
	if err == nil {
		seenDevices := make(map[string]bool)
		for _, p := range partitions {
			fstype := strings.ToLower(p.Fstype)
			if fstype == "" || fstype == "tmpfs" || fstype == "devtmpfs" ||
				fstype == "devfs" || fstype == "proc" || fstype == "sysfs" ||
				fstype == "fuseblk" || fstype == "fuse" || strings.HasPrefix(fstype, "fuse.") ||
				fstype == "squashfs" || fstype == "overlay" || fstype == "ramfs" ||
				fstype == "autofs" || fstype == "efivarfs" || fstype == "nsfs" ||
				fstype == "bpf" || fstype == "cgroup" || fstype == "cgroup2" ||
				fstype == "mqueue" || fstype == "debugfs" || fstype == "tracefs" ||
				fstype == "securityfs" || fstype == "pstore" || fstype == "hugetlbfs" ||
				fstype == "configfs" || fstype == "binfmt_misc" {
				continue
			}
			// snap などの squashfs は /dev/loop* を使う。実ディスクではないため除外する。
			if strings.HasPrefix(p.Device, "/dev/loop") {
				continue
			}
			usage, err := disk.Usage(p.Mountpoint)
			if err != nil || usage.Total == 0 {
				continue
			}

			var se smartEntry
			if m.smart.checkAvailable() && p.Device != "" {
				blockDev := resolveBlockDevice(p.Device)
				if !seenDevices[blockDev] {
					seenDevices[blockDev] = true
					if refreshSmart {
						m.smart.set(blockDev, runSmartctl(blockDev))
					}
				}
				if e, ok := m.smart.get(blockDev); ok {
					se = e
				}
			}

			disks = append(disks, status.DiskInfo{
				Path:         p.Mountpoint,
				Total:        usage.Total,
				Used:         usage.Used,
				Percent:      usage.UsedPercent,
				Temp:         se.temp,
				Health:       se.health,
				Model:        se.model,
				Serial:       se.serial,
				WriteBytes:   se.writeBytes,
				PowerOnHours: se.powerOnHours,
				RotationRate: se.rotationRate,
			})
			totalDiskUsed += usage.Used
			totalDiskTotal += usage.Total
		}
	}

	if len(disks) > 0 && totalDiskTotal > 0 {
		totalDiskPercent = float64(totalDiskUsed) / float64(totalDiskTotal) * 100
	} else if diskInfo, err := disk.Usage(diskPath); err == nil && diskInfo != nil {
		disks = []status.DiskInfo{{
			Path:    diskPath,
			Total:   diskInfo.Total,
			Used:    diskInfo.Used,
			Percent: diskInfo.UsedPercent,
		}}
		totalDiskUsed = diskInfo.Used
		totalDiskTotal = diskInfo.Total
		totalDiskPercent = diskInfo.UsedPercent
	} else if m.logger != nil {
		m.logger.Warn("ディスク情報取得失敗: %s (%v)", diskPath, err)
	}

	// ---------- Host ----------
	hostInfo, hostErr := host.Info()
	if hostErr != nil {
		if m.logger != nil {
			m.logger.Warn("ホスト情報取得失敗: %v", hostErr)
		}
		hostInfo = &host.InfoStat{Uptime: 0}
	}

	// ---------- Load Average ----------
	var loadAvg []float64
	if avg, err := load.Avg(); err == nil && avg != nil {
		loadAvg = []float64{avg.Load1, avg.Load5, avg.Load15}
	}

	// ---------- Network ----------
	var netIO *status.NetworkIO
	var netSpeed *status.NetworkSpeed
	if counters, err := gnet.IOCounters(false); err == nil && len(counters) > 0 {
		curUp := counters[0].BytesSent
		curDown := counters[0].BytesRecv
		now := time.Now()

		netIO = &status.NetworkIO{Up: curUp, Down: curDown}

		// The previous network counters are shared state (Health and
		// Configure also touch the module), so guard the read and write
		// with the module mutex.
		m.mu.Lock()
		// Compute speed from the delta and elapsed time.
		// curUp/curDown >= prev is guaranteed above; elapsed > 0 avoids division by zero.
		if m.hasPrevNet && curUp >= m.prevNetUp && curDown >= m.prevNetDown {
			elapsed := now.Sub(m.prevNetTime).Seconds()
			if elapsed > 0 {
				upSpeed := uint64(float64(curUp-m.prevNetUp) / elapsed)
				downSpeed := uint64(float64(curDown-m.prevNetDown) / elapsed)
				netSpeed = &status.NetworkSpeed{Up: upSpeed, Down: downSpeed}
			}
		}
		m.prevNetUp = curUp
		m.prevNetDown = curDown
		m.prevNetTime = now
		m.hasPrevNet = true
		m.mu.Unlock()
	}

	// ---------- Processes ----------
	processes := m.processes.get()

	// ---------- lastError ----------
	m.mu.Lock()
	defer m.mu.Unlock()

	var lastError string
	if hostErr != nil {
		lastError = fmt.Sprintf("ホスト情報取得失敗: %v", hostErr)
	} else if cpuErr != nil {
		lastError = fmt.Sprintf("CPU情報取得失敗 (連続%d回): %v", cpuErrCount, cpuErr)
	} else if len(disks) == 0 {
		lastError = "ディスク情報取得失敗"
	} else if memErr != nil {
		lastError = fmt.Sprintf("メモリ情報取得失敗: %v", memErr)
	}
	m.lastError = lastError

	now := time.Now()
	m.status = &status.SystemStatus{
		Timestamp:     now.Unix(),
		Uptime:        hostInfo.Uptime,
		CPUUsage:      cpuVal,
		CPUPerCore:    cpuPerCore,
		CPUModel:      cpuModel,
		CPUTemp:       cpuTemp,
		LoadAverage:   loadAvg,
		Network:       netIO,
		NetworkSpeed:  netSpeed,
		MemTotal:      memInfo.Total,
		MemUsed:       memInfo.Used,
		MemPercent:    memInfo.UsedPercent,
		DiskTotal:     totalDiskTotal,
		DiskUsed:      totalDiskUsed,
		DiskPercent:   totalDiskPercent,
		Disks:         disks,
		Processes:     processes,
		LastError:     lastError,
		Fallback:      isFallback,
		CPUErrorCount: cpuErrCount,
	}
}

// ============================================================
// collectCPUTemp
// ============================================================

// minPlausibleCPUTemp is the lowest value accepted as a real CPU temperature.
// Some AMD APUs (e.g. A4-4020) report physically impossible readings such as
// 4C while idle. Anything below this floor is treated as a sensor error
// (returns 0), so the dashboard shows N/A and no bogus alert is fired.
const minPlausibleCPUTemp = 10.0

func collectCPUTemp() float64 {
	temps, err := host.SensorsTemperatures()
	if err != nil || len(temps) == 0 {
		return 0
	}

	priorityKeywords := []string{
		"package id 0", "coretemp", "k10temp", "cpu_thermal",
		"zenpower", "tctl", "tdie", "soc_thermal",
	}

	for _, kw := range priorityKeywords {
		for _, t := range temps {
			key := strings.ToLower(t.SensorKey)
			if strings.Contains(key, kw) && t.Temperature >= minPlausibleCPUTemp {
				return t.Temperature
			}
		}
	}

	for _, t := range temps {
		key := strings.ToLower(t.SensorKey)
		if (strings.Contains(key, "cpu") || strings.Contains(key, "core")) && t.Temperature >= minPlausibleCPUTemp {
			return t.Temperature
		}
	}

	for _, t := range temps {
		if t.Temperature >= minPlausibleCPUTemp {
			return t.Temperature
		}
	}

	return 0
}

// ============================================================
// resolveBlockDevice
// ============================================================
// partRe matches a partition device whose partition suffix is "p<num>"
// (NVMe / mmcblk style), e.g. /dev/nvme0n1p2, /dev/mmcblk0p1.
var partRe = regexp.MustCompile(`^(.*\d)p\d+$`)

// sdRe matches an sd/vd/hd-style partition, i.e. letters followed only by a
// partition number, e.g. sda1, vda2. It deliberately does NOT match whole
// disks such as nvme0n1 (digits in the middle) so those are kept as-is.
var sdRe = regexp.MustCompile(`^[a-z]+\d+$`)

// resolveBlockDevice maps a partition device to its parent whole disk so
// S.M.A.R.T can be queried once per physical disk.
//   - /dev/sdb1       -> /dev/sdb
//   - /dev/nvme0n1p1  -> /dev/nvme0n1
//   - /dev/nvme0n1    -> /dev/nvme0n1 (unchanged; whole disk)
//   - /dev/mmcblk0p1  -> /dev/mmcblk0
//   - /dev/mapper/... -> unchanged
func resolveBlockDevice(device string) string {
	if !strings.HasPrefix(device, "/dev/") {
		return device
	}
	if strings.HasPrefix(device, "/dev/mapper/") {
		return device
	}
	name := strings.TrimPrefix(device, "/dev/")
	// NVMe / mmcblk partitions: <base><digit>p<num>.
	if m := partRe.FindStringSubmatch(name); len(m) == 2 {
		return "/dev/" + m[1]
	}
	// sd/vd/hd partitions: <letters><num>.
	if sdRe.MatchString(name) {
		return "/dev/" + strings.TrimRight(name, "0123456789")
	}
	return device
}

// ============================================================
// runSmartctl
// ============================================================
func runSmartctl(device string) smartEntry {
	cmd := exec.Command("smartctl", "-A", "-H", "-i", "-j", device)
	out, err := cmd.Output()
	if len(out) == 0 {
		return smartEntry{}
	}
	_ = err

	// Fields are pointers/optional where absence must not be mistaken for a
	// real value (a loop device or a SMART-incapable disk must not be
	// reported as FAILED, and a missing attribute must not become 0).
	var result struct {
		Device struct {
			Model        string `json:"model_name"`
			Serial       string `json:"serial_number"`
			RotationRate int    `json:"rotation_rate"`
		} `json:"device"`
		ModelName    string `json:"model_name"`
		SerialNumber string `json:"serial_number"`
		RotationRate int    `json:"rotation_rate"`
		Temperature  struct {
			Current int `json:"current"`
		} `json:"temperature"`
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		// ATA attributes: Total_LBA_Written (241) and Power_On_Hours (9).
		ATASmartAttributes struct {
			Table []struct {
				ID    int `json:"id"`
				Raw   struct {
					Value uint64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		// NVMe health log: DataUnitsWritten is in 1000-unit (512-byte) blocks.
		NVMeLog struct {
			DataUnitsWritten uint64 `json:"data_units_written"`
			PowerOnHours     uint64 `json:"power_on_hours"`
		} `json:"nvme_smart_health_information_log"`
	}

	if jerr := json.Unmarshal(out, &result); jerr != nil {
		return smartEntry{}
	}

	e := smartEntry{
		temp:   float64(result.Temperature.Current),
		health: "UNKNOWN",
	}

	// smart_status が無い場合は UNKNOWN（通知対象外）とする。
	// これにより実ディスクでない・SMART 非対応のデバイスで誤警報しない。
	if result.SmartStatus != nil {
		if result.SmartStatus.Passed {
			e.health = "PASSED"
		} else {
			e.health = "FAILED"
		}
	}

	// Model / serial / rotation live under "device" in modern smartctl
	// JSON, but older versions put model_name/serial_number at the top level.
	e.model = result.Device.Model
	if e.model == "" {
		e.model = result.ModelName
	}
	e.serial = result.Device.Serial
	if e.serial == "" {
		e.serial = result.SerialNumber
	}
	e.rotationRate = result.Device.RotationRate
	if e.rotationRate == 0 && result.RotationRate != 0 {
		e.rotationRate = result.RotationRate
	}

	// ATA: Total_LBA_Written is LBA count (512 bytes per LBA).
	for _, attr := range result.ATASmartAttributes.Table {
		switch attr.ID {
		case 241: // Total_LBA_Written
			e.writeBytes = attr.Raw.Value * 512
		case 9: // Power_On_Hours
			e.powerOnHours = attr.Raw.Value
		}
	}

	// NVMe: data_units_written is in 1000 * 512-byte units.
	if e.writeBytes == 0 && result.NVMeLog.DataUnitsWritten > 0 {
		e.writeBytes = result.NVMeLog.DataUnitsWritten * 1000 * 512
	}
	if e.powerOnHours == 0 && result.NVMeLog.PowerOnHours > 0 {
		e.powerOnHours = result.NVMeLog.PowerOnHours
	}

	return e
}

// ============================================================
// asyncCPUCollector
// ============================================================
func (m *SystemModule) asyncCPUCollector(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			percent, err := cpu.Percent(0, false)
			if err != nil {
				if m.logger != nil {
					m.logger.Debug("CPU使用率取得失敗: %v", err)
				}
				m.cpuCollector.update(0, err)
				continue
			}
			if len(percent) == 0 {
				continue
			}
			m.cpuCollector.update(percent[0], nil)
		}
	}
}
