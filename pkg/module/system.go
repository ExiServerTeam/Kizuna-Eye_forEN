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

// SystemConfig はシステムモジュールの設定だ。
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
// smartCache は S.M.A.R.T データをキャッシュする。
// smartCache caches S.M.A.R.T data.
// ============================================================
type smartCache struct {
	mu        sync.RWMutex
	entries   map[string]smartEntry
	interval  time.Duration
	lastCheck time.Time
	available bool
	checked   bool
	// emptyStreak/emptyWarned は、無言の S.M.A.R.T 読み取り失敗を一度だけ可視化する。
	// `smartctl --scan` は ATA パススルーに必要な capability が無くても成功するため、
	// 可用性プローブだけでは「動く」と「実読み取りが全部拒否される」を区別できない。
	// 信頼できる信号は「データを全く返さないデバイス」だ。
	// emptyStreak/emptyWarned surface a silent S.M.A.R.T read failure once.
	// `smartctl --scan` succeeds even without the capability needed for ATA
	// passthrough, so the availability probe alone cannot tell "works" from
	// "every real read is denied". The reliable signal is a device that
	// returns no data at all.
	emptyStreak int
	emptyWarned bool
}

// smartEntry は 1 つのブロックデバイスのキャッシュ済み S.M.A.R.T データだ。
// ゼロ値は「未報告」を意味し、JSON からは省略される。
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

// get はデバイスのキャッシュ済み S.M.A.R.T データを返す。2 番目の戻り値は
// エントリが存在するかどうかを示し、呼び出し側が「未チェック」と「チェック済みだがデータ無し」を
// 区別できるようにする。
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

// recordResult は S.M.A.R.T 読み取りがデータを生んだかを記録し、呼び出し側が
// 一度きりの警告を出すべきかを返す。ok=false は連続回数を増やす。警告は連続ごとに
// 一度だけ発火し、一度でも成功すると再武装する。これにより恒久的に壊れた読み取りは
// 毎 interval ではなく一度だけ警告し、後日の再発は依然として報告される。
// recordResult records whether a S.M.A.R.T read produced data and reports
// whether the caller should emit a one-time warning. ok=false increments a
// streak; the warning fires once per streak and re-arms after any success, so
// a permanently broken read warns once instead of on every interval, while a
// later recurrence is still reported.
func (s *smartCache) recordResult(ok bool) (warn bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.emptyStreak = 0
		s.emptyWarned = false
		return false
	}
	s.emptyStreak++
	if !s.emptyWarned {
		s.emptyWarned = true
		return true
	}
	return false
}

// smartctlBin/sudoBin はパッケージ init で絶対パスに解決する。サービスの PATH は
// 通常信頼できるが、先に絶対パスを解決しておけば PATH エントリが実バイナリを
// 覆い隠す余地を除ける（security プラグインは firewall バイナリで既にそうしている）。
// smartctlBin/sudoBin are resolved to absolute paths at package init. The
// service PATH is normally trustworthy, but resolving an absolute path first
// removes any chance of a PATH entry shadowing the real binary (the security
// plugin already does this for the firewall binaries).
var (
	smartctlBin = resolveSystemBin("smartctl",
		"/usr/sbin/smartctl", "/sbin/smartctl", "/usr/bin/smartctl", "/bin/smartctl",
		"/usr/local/sbin/smartctl", "/usr/local/bin/smartctl")
	sudoBin = resolveSystemBin("sudo",
		"/usr/bin/sudo", "/bin/sudo", "/usr/sbin/sudo", "/sbin/sudo")
)

// resolveSystemBin は存在する最初の絶対候補を返し、どれも無い場合のみ
// exec.LookPath(name) にフォールバックする。全く見つからなければ "" を返す。
// resolveSystemBin returns the first existing absolute candidate, falling back
// to exec.LookPath(name) only when none exists. It returns "" when the binary
// cannot be found at all.
func resolveSystemBin(name string, candidates ...string) string {
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// smartctlOnce/smartctlPrefix は smartctl の呼び出し方法をメモ化する。
// 優先順:
//  1. smartctl だけへのパスワード無し sudo（A-4 以前の旧経路）。注: A-4 unit
//     （NoNewPrivileges=yes）では sudo が必ず失敗するため、ここは不活性になる。
//     その場合 S.M.A.R.T は CAP_SYS_RAWIO + 素のバイナリに依存する。
//  2. 絶対パスに解決した素のバイナリ。
//
// smartctlOnce/smartctlPrefix memoize how to invoke smartctl.
// Preference order:
//  1. passwordless sudo for smartctl only (legacy pre-A-4 path). NOTE: under
//     the A-4 unit (NoNewPrivileges=yes) sudo always fails, so this branch is
//     inert there; S.M.A.R.T then relies on CAP_SYS_RAWIO + the plain binary.
//  2. the plain binary, resolved to an absolute path.
var (
	smartctlOnce   sync.Once
	smartctlPrefix []string
)

// smartctlCommand は smartctl の exec.Cmd を組み立て、使える場合は sudo を選ぶ。
// smartctlCommand builds an exec.Cmd for smartctl, choosing sudo when usable.
func smartctlCommand(args ...string) *exec.Cmd {
	smartctlOnce.Do(func() {
		if sudoBin != "" && smartctlBin != "" {
			// 非対話プローブ。NOPASSWD ルールが存在する場合のみ成功する。
			// Non-interactive probe; succeeds only if the NOPASSWD rule exists.
			if err := exec.Command(sudoBin, "-n", smartctlBin, "--version").Run(); err == nil {
				smartctlPrefix = []string{sudoBin, "-n", smartctlBin}
			}
		}
	})
	if len(smartctlPrefix) > 0 {
		full := append(append([]string{}, smartctlPrefix[1:]...), args...)
		return exec.Command(smartctlPrefix[0], full...)
	}
	bin := smartctlBin
	if bin == "" {
		bin = "smartctl" // 見つからない。exec に大声で失敗させる。
		// 見つからない場合は exec に明確に失敗させる
		// not found; let exec fail loudly
	}
	return exec.Command(bin, args...)
}

// smartctlAvailable は S.M.A.R.T を実際に問い合わせられるかを返す。
// バイナリが見つかるだけでは不十分だ。sudoers ルールの無い非 root agent は
// smartctl を見つけても、すべての読み取りが失敗する。そこで選択した呼び出し経路で
// 実クエリ（smartctl --scan）をプローブし、成功を要求する。
// smartctlAvailable reports whether S.M.A.R.T can actually be queried.
// Merely finding the binary is not enough: a non-root agent without a sudoers
// rule would find smartctl but every read would fail. So probe a real query
// (smartctl --scan) through the chosen invocation path and require success.
func smartctlAvailable() bool {
	if smartctlBin == "" {
		// smartctl がどこにも無い。sudo 経路も助けにならない。
		// No smartctl anywhere; the sudo path cannot help either.
		return false
	}
	// `smartctl --scan` はデバイスを列挙し、特定デバイスへの生ディスクアクセスを
	// 必要とせず 0 で終了する。選択した経路（素か sudo -n）でバイナリが動くことを確認する。
	// `smartctl --scan` lists devices and exits 0 without needing raw disk
	// access to a specific device; it confirms the binary runs via the chosen
	// path (plain or sudo -n).
	cmd := smartctlCommand("--scan")
	return cmd.Run() == nil
}

func (s *smartCache) checkAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checked {
		return s.available
	}
	s.checked = true
	s.available = smartctlAvailable()
	return s.available
}

// ============================================================
// ★ Phase 6: processCache
//
// processCache は Top N プロセスをキャッシュする。
//   - 3 秒ごとに更新する（毎秒呼ぶのは高コスト）。
//   - CPU% は前回サンプルからの差分で計算する。
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

	// CPU% 差分計算用
	// For CPU% delta calculation
	lastCPUTimes   map[int32]float64 // PID -> 合計 CPU 秒 / total CPU seconds
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
// SystemModule（システム監視モジュール）
// SystemModule (system monitoring module)
// ============================================================
type SystemModule struct {
	config     *SystemConfig
	logger     Logger
	mu         sync.RWMutex
	status     *status.SystemStatus
	lastError  string
	isFallback bool

	cpuModel string

	// フェーズ4
	// Phase 4
	prevNetUp   uint64
	prevNetDown uint64
	prevNetTime time.Time
	hasPrevNet  bool

	smart *smartCache

	// ★ フェーズ6
	// ★ Phase 6
	processes *processCache

	cpuCollector *cpuCollector
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// ---------- CPU コレクタ ----------
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

// ---------- コンストラクタ ----------
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

// ---------- Module インターフェース ----------
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

// ---------- Init（初期化）----------
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
		// 「検出」は「収集中」を主張してはならない。smartctl --scan は
		// 実際の読み取りが全て拒否されても成功する（A-4 capability の罠）。
		// 権威ある信号は下の収集経路から出す。
		// "Detected" must not claim "collecting": smartctl --scan succeeds
		// even when every real read is denied (the A-4 capability trap). The
		// authoritative signal is emitted from the collection path below.
		if m.logger != nil {
			m.logger.Info("smartctl 検出: ディスクS.M.A.R.T の取得を試行します")
		}
	} else {
		// 「未インストール」と「インストール済みだが全読み取り失敗」を区別する。
		// 後者が A-4 の罠だ。agent は非 root で動き、CAP_SYS_RAWIO が無いと
		// smartctl はデバイスを見つける（--scan は成功）が、ATA パススルー読み取りは
		// すべて拒否される。「未検出」とだけ報告すると、ディスク健康状態・温度・
		// 書込量・通電時間（設計 4.2 / 4.3）の無言の欠落を隠してしまい、これは
		// まさに運用者がダッシュボードで見えない障害だ。
		// Distinguish "not installed" from "installed but every read fails".
		// The second case is the A-4 trap: the agent runs as a non-root user
		// and, without CAP_SYS_RAWIO, smartctl finds the devices (--scan
		// succeeds) but every ATA passthrough read is denied. Reporting only
		// "not detected" hid a silent loss of disk health / temperature /
		// write-bytes / power-on-hours (design 4.2 / 4.3), which is exactly
		// the failure operators cannot see on the dashboard.
		if _, lerr := exec.LookPath("smartctl"); lerr == nil {
			if m.logger != nil {
				m.logger.Warn("smartctl は存在しますが S.M.A.R.T を取得できません（権限不足の可能性）。agent の権限を確認してください")
			}
		} else if m.logger != nil {
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

// ---------- Run（実行ループ）----------
// ---------- Run ----------
func (m *SystemModule) Run(ctx context.Context) error {
	m.updateStatus()
	return nil
}

// ---------- Configure（設定適用）----------
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

// ---------- HealthCheckable（健全性検査）----------
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

// ---------- Shutdownable（停止処理）----------
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

// ---------- 内部メソッド ----------
// ---------- Internal methods ----------
func (m *SystemModule) GetStatus() *status.SystemStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.status == nil {
		return nil
	}
	// ディープコピー: SystemStatus はスライス/ポインタ（Disks、Processes、
	// CPUPerCore、LoadAverage、Network、NetworkSpeed）を含む。浅いコピーだと
	// 保持中の status と背後の配列を共有し、結果を変更した呼び出し側が
	// 生きた status を壊す（または updateStatus と競合する）恐れがある。
	// Deep copy: SystemStatus contains slices/pointers (Disks, Processes,
	// CPUPerCore, LoadAverage, Network, NetworkSpeed). A shallow copy would
	// share their backing arrays with the stored status, so a caller that
	// mutates the result could corrupt the live status (or race with
	// updateStatus).
	return m.status.Clone()
}

// StatusTimestamp は最新収集 status のタイムスタンプをコピーせずに返す。
// 安価（RLock のみ）で、送信 interval が収集 interval より短いときに agent が
// 未変更 status の再送をスキップできる。これにより tick ごとのディープコピー +
// JSON エンコード + ブロードキャストを避ける。
// StatusTimestamp returns the timestamp of the latest collected status
// without copying it. It is cheap (RLock only) and lets the agent skip
// re-sending an unchanged status when its send interval is shorter than the
// collection interval, avoiding a deep copy + JSON encode + broadcast per tick.
func (m *SystemModule) StatusTimestamp() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.status == nil {
		return 0
	}
	return m.status.Timestamp
}

func (m *SystemModule) getStaleThresholdLocked() time.Duration {
	if m.config.StaleThreshold > 0 {
		return time.Duration(m.config.StaleThreshold) * time.Second
	}
	return time.Duration(m.config.Interval*3) * time.Second
}

// ============================================================
// updateStatus はシステム情報を収集する。
// updateStatus collects system information.
// ============================================================
func (m *SystemModule) updateStatus() {
	m.mu.RLock()
	diskPath := m.config.DiskPath
	isFallback := m.isFallback
	cpuModel := m.cpuModel
	m.mu.RUnlock()

	// ---------- CPU ----------
	// gopsutil は全体（percpu=false）とコア単位（percpu=true）のサンプリングで
	// 別々のベースラインを保持するため、この 2 つの呼び出しは競合しない。
	// gopsutil keeps separate baselines for overall (percpu=false) and
	// per-core (percpu=true) sampling, so these two calls do not race.
	cpuVal, cpuErr := m.cpuCollector.get()
	cpuErrCount := m.cpuCollector.getErrorCount()
	cpuPerCore, _ := cpu.Percent(0, true)
	cpuTemp := collectCPUTemp()

	// ---------- メモリ ----------
	// ---------- Memory ----------
	memInfo, memErr := mem.VirtualMemory()
	if memErr != nil {
		if m.logger != nil {
			m.logger.Warn("メモリ情報取得失敗: %v", memErr)
		}
		memInfo = &mem.VirtualMemoryStat{Total: 0, Used: 0, UsedPercent: 0}
	}

	// ---------- ディスク ----------
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
			// squashfs mounts such as snap use /dev/loop*; skip them, they are
			// not real disks.
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
					// health 文字列の無いエントリは読み取りが何も生まなかった
					// （権限拒否、非対応デバイス）ことを意味する。空の S.M.A.R.T を
					// 黙って表示せず、一度だけ可視化する。
					// An entry with no health string means the read produced
					// nothing (permission denied, unsupported device). Surface
					// it once instead of showing an empty S.M.A.R.T silently.
					if m.smart.recordResult(e.health != "") && m.logger != nil {
						m.logger.Warn("ディスク %s の S.M.A.R.T を取得できません（権限不足の可能性。agent に CAP_SYS_RAWIO が必要です）", blockDev)
					}
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
	} else {
		diskInfo, uerr := disk.Usage(diskPath)
		if uerr == nil && diskInfo != nil {
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
			// この分岐では先の Partitions エラーは nil なので、disk.Usage の
			// エラーを報告する。古い nil を出すのではなく実際の失敗を指し示す。
			// Report the disk.Usage error, not the earlier Partitions error
			// (which is nil on this branch), so the log points at the real
			// failure instead of printing a stale nil.
			if uerr != nil {
				m.logger.Warn("ディスク情報取得失敗: %s (%v)", diskPath, uerr)
			} else {
				m.logger.Warn("ディスク情報取得失敗: %s", diskPath)
			}
		}
	}

	// ---------- ホスト ----------
	// ---------- Host ----------
	hostInfo, hostErr := host.Info()
	if hostErr != nil {
		if m.logger != nil {
			m.logger.Warn("ホスト情報取得失敗: %v", hostErr)
		}
		hostInfo = &host.InfoStat{Uptime: 0}
	}

	// ---------- ロードアベレージ ----------
	// ---------- Load Average ----------
	var loadAvg []float64
	if avg, err := load.Avg(); err == nil && avg != nil {
		loadAvg = []float64{avg.Load1, avg.Load5, avg.Load15}
	}

	// ---------- ネットワーク ----------
	// ---------- Network ----------
	var netIO *status.NetworkIO
	var netSpeed *status.NetworkSpeed
	if counters, err := gnet.IOCounters(false); err == nil && len(counters) > 0 {
		curUp := counters[0].BytesSent
		curDown := counters[0].BytesRecv
		now := time.Now()

		netIO = &status.NetworkIO{Up: curUp, Down: curDown}

		// 前回のネットワークカウンタは共有状態（Health と Configure もモジュールに
		// 触れる）なので、読み書きをモジュールミューテックスで保護する。
		// The previous network counters are shared state (Health and
		// Configure also touch the module), so guard the read and write
		// with the module mutex.
		m.mu.Lock()
		// 差分と経過時間から速度を計算する。
		// curUp/curDown >= prev は上で保証済み。elapsed > 0 でゼロ除算を避ける。
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

	// ---------- プロセス ----------
	// ---------- Processes ----------
	processes := m.processes.get()

	// ---------- lastError（直近エラー）----------
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
// collectCPUTemp（CPU 温度の収集）
// collectCPUTemp (collect CPU temperature)
// ============================================================

// minPlausibleCPUTemp は実際の CPU 温度として受け入れる最低値だ。
// 一部の AMD APU（例: A4-4020）はアイドル時に 4C のような物理的にありえない
// 値を報告する。この下限未満はセンサエラーとして扱い（0 を返す）、
// ダッシュボードは N/A を表示し、偽のアラートを発火しない。
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
// partRe はパーティション接尾辞が "p<num>" のパーティションデバイスに一致する
// （NVMe / mmcblk 形式）。例: /dev/nvme0n1p2、/dev/mmcblk0p1。
// partRe matches a partition device whose partition suffix is "p<num>"
// (NVMe / mmcblk style), e.g. /dev/nvme0n1p2, /dev/mmcblk0p1.
var partRe = regexp.MustCompile(`^(.*\d)p\d+$`)

// sdRe は sd/vd/hd 形式のパーティション、すなわち文字の後にパーティション番号だけが
// 続くもの（例: sda1、vda2）に一致する。nvme0n1（途中に数字）のような丸ごとの
// ディスクには意図的に一致させず、そのまま保つ。
// sdRe matches an sd/vd/hd-style partition, i.e. letters followed only by a
// partition number, e.g. sda1, vda2. It deliberately does NOT match whole
// disks such as nvme0n1 (digits in the middle) so those are kept as-is.
var sdRe = regexp.MustCompile(`^[a-z]+\d+$`)

// resolveBlockDevice はパーティションデバイスを親の丸ごとのディスクへ対応付け、
// S.M.A.R.T を物理ディスクごとに 1 回問い合わせられるようにする。
//   - /dev/sdb1       -> /dev/sdb
//   - /dev/nvme0n1p1  -> /dev/nvme0n1
//   - /dev/nvme0n1    -> /dev/nvme0n1 (unchanged; whole disk)
//   - /dev/mmcblk0p1  -> /dev/mmcblk0
//   - /dev/mapper/... -> unchanged
//
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
	// RAID、device-mapper、loop、光学その他の仮想デバイスは、末尾の数字が
	// 名前の一部である丸ごとのデバイスだ（例: md0、dm-0、sr0、nbd0）。
	// パーティション番号ではない。数字を剥がすと smartctl が存在しない
	// パスへ問い合わせることになる。
	// これらの接頭辞は sd/hd/vd ディスクのパーティションを決して示さないため、
	// 末尾の数字は丸ごとのデバイス名の一部だ。そのまま返す。
	// RAID, device-mapper, loop, optical and other virtual devices are whole
	// devices whose trailing digits are part of the name (e.g. md0, dm-0,
	// sr0, nbd0), not a partition number. Stripping the digits would query
	// smartctl against a non-existent path.
	// These prefixes never denote a partition of an sd/hd/vd disk, so a
	// trailing digit is part of the whole-device name. Return unchanged.
	for _, pfx := range []string{"md", "dm-", "loop", "ram", "zram", "sr", "nbd", "rbd"} {
		if strings.HasPrefix(name, pfx) {
			return device
		}
	}
	// NVMe / mmcblk のパーティション: <base><digit>p<num>。
	// NVMe / mmcblk partitions: <base><digit>p<num>.
	if m := partRe.FindStringSubmatch(name); len(m) == 2 {
		return "/dev/" + m[1]
	}
	// パーティション接尾辞の無い丸ごとの NVMe / mmcblk デバイスも末尾が数字だ
	// （例: /dev/nvme0n1、/dev/mmcblk0）。これらは丸ごとのディスクであり
	// パーティションではない。下の sdRe は "mmcblk0"（文字+数字）に一致し
	// "/dev/mmcblk" という存在しないデバイスにしてしまい、smartctl が黙って
	// 何も問い合わせなくなる。そのまま返す。
	// A whole NVMe / mmcblk device (no partition suffix) still ends in a
	// digit, e.g. /dev/nvme0n1 or /dev/mmcblk0. Those are whole disks, not
	// partitions: sdRe below would match "mmcblk0" (letters+digits) and turn
	// it into "/dev/mmcblk", a device that does not exist, so smartctl would
	// silently query nothing. Return them unchanged.
	if strings.HasPrefix(name, "nvme") || strings.HasPrefix(name, "mmcblk") {
		return device
	}
	// sd/vd/hd のパーティション: <letters><num>。
	// sd/vd/hd partitions: <letters><num>.
	if sdRe.MatchString(name) {
		return "/dev/" + strings.TrimRight(name, "0123456789")
	}
	return device
}

// ============================================================
// runSmartctl（smartctl の実行）
// runSmartctl (run smartctl)
// ============================================================
func runSmartctl(device string) smartEntry {
	cmd := smartctlCommand("-A", "-H", "-i", "-j", device)
	out, err := cmd.Output()
	if len(out) == 0 {
		return smartEntry{}
	}
	_ = err

	// フィールドは「不在を実値と取り違えてはならない」箇所でポインタ/省略可に
	// する（loop デバイスや SMART 非対応ディスクを FAILED と報告してはならず、
	// 欠落した属性が 0 になってはならない）。
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
		// ATA 属性: Total_LBA_Written (241) と Power_On_Hours (9)。
		// ATA attributes: Total_LBA_Written (241) and Power_On_Hours (9).
		ATASmartAttributes struct {
			Table []struct {
				ID  int `json:"id"`
				Raw struct {
					Value uint64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		// NVMe ヘルスログ: DataUnitsWritten は 1000 単位（512 バイト）ブロック。
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
	// When smart_status is absent, use UNKNOWN (not alertable), so a
	// non-disk or SMART-incapable device does not raise a false alert.
	if result.SmartStatus != nil {
		if result.SmartStatus.Passed {
			e.health = "PASSED"
		} else {
			e.health = "FAILED"
		}
	}

	// Model / serial / rotation は最近の smartctl JSON では "device" の下に
	// あるが、古いバージョンは model_name/serial_number をトップレベルに置く。
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

	// ATA: Total_LBA_Written は LBA 数（1 LBA = 512 バイト）。
	// ATA: Total_LBA_Written is LBA count (512 bytes per LBA).
	for _, attr := range result.ATASmartAttributes.Table {
		switch attr.ID {
		case 241: // Total_LBA_Written
			e.writeBytes = attr.Raw.Value * 512
		case 9: // Power_On_Hours
			e.powerOnHours = attr.Raw.Value
		}
	}

	// NVMe: data_units_written は 1000 * 512 バイト単位。
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
// asyncCPUCollector（非同期 CPU コレクタ）
// asyncCPUCollector (asynchronous CPU collector)
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
