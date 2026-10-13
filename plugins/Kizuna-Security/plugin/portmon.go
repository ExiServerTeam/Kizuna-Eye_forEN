package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/module"
)

// PortMonitor は新たに待ち受け(LISTEN/UNCONN)を開始したポートを検知する。
//
// 「新規リッスンポート」はバックドアや意図しないサービスの起動を示すことが
// あるため、ss で現在の待ち受け一覧を取得し、既知のベースラインと比較して
// 新しく現れたポートだけを通知する。ベースラインはファイルに永続化し、
// 再起動で全ポートを「新規」と誤検知しないようにする。
type PortMonitor struct {
	baselinePath string
	logger       module.Logger
	emitFn       func(module.SecurityEvent)

	mu          sync.Mutex
	known       map[string]portInfo
	loaded      bool
	hasBaseline bool

	// missingNotified は「ss を実行できない」警告を繰り返さないためのフラグ（F-3）。
	missingNotified bool
}

type portInfo struct {
	Proto   string `json:"proto"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type portState struct {
	Ports []portInfo `json:"ports"`
}

func NewPortMonitor(baselinePath string, logger module.Logger, emitFn func(module.SecurityEvent)) *PortMonitor {
	return &PortMonitor{
		baselinePath: baselinePath,
		logger:       logger,
		emitFn:       emitFn,
		known:        make(map[string]portInfo),
	}
}

func portKey(p portInfo) string {
	return fmt.Sprintf("%s/%d", p.Proto, p.Port)
}

func (m *PortMonitor) Check() {
	ports, err := listPortsFn()
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("Kizuna-Security: 待ち受けポートを取得できません: %v", err)
		}
		// F-3: 検知が止まったことを黙って見過ごさない。原因が解消するまで
		// 毎回通知しないよう、初回だけ通知する。
		m.mu.Lock()
		notified := m.missingNotified
		m.missingNotified = true
		m.mu.Unlock()
		if !notified && m.emitFn != nil {
			m.emitFn(module.SecurityEvent{
				Category:  "listen_port",
				Level:     "warning",
				Title:     "新規リッスンポートの検知が停止しています",
				Message:   fmt.Sprintf("ss の実行に失敗しました（%v）。ss のパスと実行権限を確認してください。", err),
				Source:    "ss",
				Timestamp: time.Now(),
			})
		}
		return
	}
	m.mu.Lock()
	m.missingNotified = false
	m.mu.Unlock()

	current := make(map[string]portInfo, len(ports))
	for _, p := range ports {
		current[portKey(p)] = p
	}

	m.mu.Lock()
	if !m.loaded {
		m.loaded = true
		m.hasBaseline = m.loadBaselineLocked()
	}
	// 初回（ベースライン未作成）は現在のポートを記録するだけで通知しない。
	if !m.hasBaseline {
		m.hasBaseline = true
		m.known = current
		m.saveBaselineLocked()
		m.mu.Unlock()
		return
	}

	var newPorts []portInfo
	for k, p := range current {
		if _, ok := m.known[k]; !ok {
			newPorts = append(newPorts, p)
		}
	}
	m.known = current
	m.saveBaselineLocked()
	m.mu.Unlock()

	sort.Slice(newPorts, func(i, j int) bool {
		if newPorts[i].Port != newPorts[j].Port {
			return newPorts[i].Port < newPorts[j].Port
		}
		return newPorts[i].Proto < newPorts[j].Proto
	})

	for _, p := range newPorts {
		m.emitFn(module.SecurityEvent{
			Category:  "listen_port",
			Level:     "warning",
			Title:     "新規リッスンポートを検知",
			Message:   fmt.Sprintf("%s ポート %d が新たに待ち受けを開始しました（%s）。", strings.ToUpper(p.Proto), p.Port, p.Address),
			Source:    "ss",
			Timestamp: time.Now(),
		})
	}
}

// ssCandidates は iproute2 の ss の絶対パス候補（優先順）。
// F-3: 従来は exec.Command("ss", ...) と PATH 解決に依存していたため、
// PATH に書き込める攻撃者（~/.local/bin 等）が「何も出力しない偽 ss」を
// 置くだけで新規リッスンポート検知を恒久停止できた。絶対パスのみを使う。
var ssCandidates = []string{"/usr/sbin/ss", "/sbin/ss", "/usr/bin/ss", "/bin/ss"}

var (
	ssPathOnce sync.Once
	ssPath     string
)

// resolveSSPath は ss の絶対パスを返す。見つからなければ "" を返す。
// LookPath の結果が相対パスだった場合は採用しない（PATH 依存を残さないため）。
func resolveSSPath() string {
	ssPathOnce.Do(func() {
		for _, p := range ssCandidates {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				ssPath = p
				return
			}
		}
		if p, err := exec.LookPath("ss"); err == nil && filepath.IsAbs(p) {
			ssPath = p
		}
	})
	return ssPath
}

// listPortsFn は Check から呼ばれる間接参照（テストで差し替える）。
var listPortsFn = listListeningPorts

// listListeningPorts runs `ss -H -tuln` and parses the listening sockets.
func listListeningPorts() ([]portInfo, error) {
	ss := resolveSSPath()
	if ss == "" {
		return nil, fmt.Errorf("ss が見つかりません（%s 等の絶対パスを確認してください）", ssCandidates[0])
	}
	out, err := exec.Command(ss, "-H", "-tuln").Output()
	if err != nil {
		return nil, err
	}
	var ports []portInfo
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := parseSSLine(line); ok {
			ports = append(ports, p)
		}
	}
	return ports, nil
}

func parseSSLine(line string) (portInfo, bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return portInfo{}, false
	}
	proto := fields[0]
	if proto != "tcp" && proto != "udp" {
		return portInfo{}, false
	}
	local := fields[4]
	idx := strings.LastIndex(local, ":")
	if idx < 0 {
		return portInfo{}, false
	}
	addr := local[:idx]
	port, err := strconv.Atoi(local[idx+1:])
	if err != nil {
		return portInfo{}, false
	}
	if addr == "" {
		addr = "*"
	}
	return portInfo{Proto: proto, Address: addr, Port: port}, true
}

func (m *PortMonitor) loadBaselineLocked() bool {
	data, err := os.ReadFile(m.baselinePath)
	if err != nil {
		return false
	}
	var st portState
	if err := json.Unmarshal(data, &st); err != nil {
		return false
	}
	for _, p := range st.Ports {
		m.known[portKey(p)] = p
	}
	return true
}

func (m *PortMonitor) saveBaselineLocked() {
	if m.baselinePath == "" {
		return
	}
	ports := make([]portInfo, 0, len(m.known))
	for _, p := range m.known {
		ports = append(ports, p)
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Port != ports[j].Port {
			return ports[i].Port < ports[j].Port
		}
		return ports[i].Proto < ports[j].Proto
	})
	data, err := json.MarshalIndent(portState{Ports: ports}, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(m.baselinePath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0700)
	}
	// fsutil: 一時ファイル→fsync→rename（L-12: 9箇所に重複していた処理）。
	_ = fsutil.WriteFileAtomic(m.baselinePath, data, 0600)
}
