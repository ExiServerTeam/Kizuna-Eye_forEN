package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"Kizuna-Eye/internal/api"
	"Kizuna-Eye/internal/auth"
	"Kizuna-Eye/pkg/alert"
	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/notify"
	"Kizuna-Eye/pkg/status"
)

var upgrader = websocket.Upgrader{
	// Allow same-origin browser connections, and non-browser clients
	// (the Agent) which send no Origin header. Rejecting cross-origin
	// requests prevents cross-site WebSocket hijacking (CSWSH), where a
	// malicious page opens /ws with the victim's session cookie.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser client (e.g. the Agent)
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// acceptAgentPayload reports whether an inbound /ws payload may be treated as
// trusted agent data (stored as the last status and rebroadcast to browsers),
// and whether the connection should be promoted to the agent by the legacy
// tokenless heuristic.
//
// isAgent is true once the connection has been authenticated as the agent
// (valid shared token, or already promoted). canPromote is true only for a
// non-browser connection that declared role=agent while no token is
// configured. A plain browser (isAgent=false, canPromote=false) must never be
// accepted, so it cannot spoof metrics/history or hijack the agent.
func acceptAgentPayload(isAgent, isStatus, canPromote bool) (accept, promote bool) {
	if isAgent {
		return true, false
	}
	if isStatus && canPromote {
		return true, true
	}
	return false, false
}

// eligibleForHeuristicPromotion reports whether a /ws connection may become
// the agent through the legacy tokenless heuristic. It requires that no token
// is configured, that the client declared role=agent, and that the client is
// NOT a browser. Browsers always send an Origin header on the WebSocket
// handshake; the Go agent does not. Without the Origin check, any logged-in
// browser could open /ws?role=agent and impersonate the agent.
func eligibleForHeuristicPromotion(agentToken string, isAgentRole bool, origin string) bool {
	return agentToken == "" && isAgentRole && origin == ""
}

// sanitizeLogField strips control characters (CR, LF, tab and other C0/C1
// controls) from an untrusted value before it is written to a log. Plugin
// events carry attacker-influenced text (log lines parsed from auth.log);
// without this a crafted value could inject forged log lines.
func sanitizeLogField(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// Drop other control characters (NUL, ESC, ANSI introducers, ...).
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// writeDashboardJSON writes JSON with a Content-Type header.
// Cache-Control: no-store keeps sensitive API responses (log lines with
// usernames/IPs, masked config, module paths, session-scoped data) out of
// shared proxy and browser caches. The auth handler already sets no-store;
// this keeps the dashboard APIs consistent.
func writeDashboardJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// handleSecurityEvent turns an agent "security_alert" event into a
// notification (Discord etc.) and an alert-history entry.
func handleSecurityEvent(msg []byte, engine *alert.Engine, lg *logger.Logger) {
	var env struct {
		Event    string `json:"event"`
		Plugin   string `json:"plugin"`
		Category string `json:"category"`
		Level    string `json:"level"`
		Title    string `json:"title"`
		Message  string `json:"message"`
		Actor    string `json:"actor"`
		IP       string `json:"ip"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		return
	}
	if env.Event != "security_alert" || engine == nil {
		return
	}

	level := notify.LevelInfo
	switch strings.ToLower(env.Level) {
	case "critical":
		level = notify.LevelCritical
	case "warning":
		level = notify.LevelWarning
	case "success":
		level = notify.LevelSuccess
	}

	icon := "🔐"
	if level == notify.LevelCritical {
		icon = "🚨"
	} else if level == notify.LevelWarning {
		icon = "⚠️"
	}

	body := env.Message
	if env.Actor != "" || env.IP != "" {
		parts := make([]string, 0, 2)
		if env.Actor != "" {
			parts = append(parts, "ユーザー: "+env.Actor)
		}
		if env.IP != "" {
			parts = append(parts, "接続元: "+env.IP)
		}
		if len(parts) > 0 {
			body += " (" + strings.Join(parts, " / ") + ")"
		}
	}

	engine.ReportAlert(&notify.Alert{
		Type:      "security_" + env.Category,
		Level:     level,
		Icon:      icon,
		Title:     env.Title,
		Message:   body,
		Timestamp: time.Now(),
	}, "agent", false) // agent-reported: derived from a forgeable log line

	if lg != nil {
		lg.Info("セキュリティアラート: [%s] %s", sanitizeLogField(env.Level), sanitizeLogField(env.Title))
	}
}

// ============================================================
// wsClient wraps a WebSocket connection with its own write mutex.
// gorilla/websocket allows only one concurrent writer per connection,
// but writes to *different* connections may proceed in parallel. A
// per-connection mutex prevents one slow client from blocking every
// other client (and the agent).
// ============================================================
type wsClient struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	// authed is true when the connection carried a valid session at upgrade
	// time. In public viewer mode an unauthenticated client only receives
	// sanitized statuses and no security events.
	authed bool
}

func (c *wsClient) writeMessage(messageType int, data []byte, timeout time.Duration) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(timeout))
	return c.conn.WriteMessage(messageType, data)
}

// writeControl sends a control frame (ping/pong/close) under the same write
// mutex used by writeMessage, so a ping cannot interleave with a broadcast.
func (c *wsClient) writeControl(messageType int, data []byte, deadline time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteControl(messageType, data, deadline)
}

// ============================================================
// Hub manages WebSocket clients.
// ============================================================
type Hub struct {
	sync.RWMutex
	clients    map[*wsClient]bool
	maxClients int
	log        *logger.Logger
	lastStatus *status.SystemStatus

	// agentToken authenticates agent connections. Empty disables token
	// authentication (legacy heuristic detection only).
	agentToken string

	// viewerMode filters what unauthenticated browser clients receive: when
	// true, statuses are sanitized and events are withheld for clients that
	// did not present a session. Set once during startup.
	viewerMode bool

	agentConn *wsClient

	// Callbacks are set once during startup and read from WS goroutines.
	// They are guarded by the embedded RWMutex so a late assignment cannot
	// race with a reader (use SetCallbacks / the onX accessors).
	onAgentDisconnect func()
	onAgentStatus     func(*status.SystemStatus)
	onAgentEvent      func([]byte)

	// Compact samples (ring, chronological). Storing only the charted
	// fields keeps memory small so ~1h of 1s-interval data fits.
	samples    []api.HistorySample
	maxSamples int

	// maxViewers caps the number of *unauthenticated* WebSocket clients.
	// The /ws endpoint is reachable without a session, so without a separate
	// cap an attacker could open maxClients anonymous connections and starve
	// the Agent (and every real browser) of the fixed client slots. Authenticated
	// clients (the Agent and logged-in users) are never counted against this
	// limit and are admitted as long as maxClients allows, so the monitoring
	// feed cannot be denied by anonymous connection exhaustion.
	maxViewers int

	// Flood detection: a burst of rejected connections is the signature of a
	// connection-exhaustion DoS. Without this, the dashboard only logged the
	// rejections and never raised an alert, so an operator could not tell that
	// the service was under attack. onFlood is called (at most once per
	// floodWindow) when rejections cross floodThreshold inside a window.
	onFlood func(rejected int, detail string)
	// floodMu guards the counters below.
	floodMu       sync.Mutex
	floodCount    int
	floodWindowAt time.Time

	// onAuthReject is called (at most once per authRejectWindow and
	// per reason) when suspicious agent connections are rejected.
	// Without it, token brute-force / agent hijack attempts were only
	// WARN lines in the log and never reached the operator.
	onAuthReject func(reason string, count int)
	// authRejectMu guards authRejects below.
	authRejectMu sync.Mutex
	authRejects  map[string]*authRejectCounter
}

// floodThreshold is how many rejected connections within floodWindow raise a
// flood alert, and floodWindow is the measurement window. 200 rejections in 10
// seconds is far above normal operation but well below what a real flood
// produces (the DoS tests sent tens of thousands).
const (
	floodThreshold = 200
	floodWindow    = 10 * time.Second
)

// noteRejected records a rejected connection and fires onFlood when the
// threshold is crossed. The window resets after each alert so the alert is
// raised at most once per floodWindow, not once per rejection.
func (h *Hub) noteRejected(detail string) {
	h.floodMu.Lock()
	now := time.Now()
	if h.floodWindowAt.IsZero() || now.Sub(h.floodWindowAt) > floodWindow {
		h.floodWindowAt = now
		h.floodCount = 0
	}
	h.floodCount++
	count := h.floodCount
	fire := count == floodThreshold // exactly once per window
	cb := h.onFlood
	h.floodMu.Unlock()
	if fire && cb != nil {
		cb(count, detail)
	}
}

// authRejectThreshold is how many suspicious agent connections with the
// SAME reason within authRejectWindow raise one alert, and authRejectWindow
// is the measurement window. 10 attempts in 60s is well above normal
// (a healthy agent connects once) but below a real brute-force burst.
const (
	authRejectThreshold = 10
	authRejectWindow    = 60 * time.Second
)

// authRejectCounter tracks one rejection reason's windowed count.
type authRejectCounter struct {
	count    int
	windowAt time.Time
}

// noteAuthReject records a suspicious agent connection and fires
// onAuthReject when the threshold is reached. The window resets after
// each alert so the alert is raised at most once per window per reason.
func (h *Hub) noteAuthReject(reason string) {
	h.authRejectMu.Lock()
	if h.authRejects == nil {
		h.authRejects = make(map[string]*authRejectCounter)
	}
	now := time.Now()
	c := h.authRejects[reason]
	if c == nil || now.Sub(c.windowAt) > authRejectWindow {
		c = &authRejectCounter{windowAt: now}
		h.authRejects[reason] = c
	}
	c.count++
	count := c.count
	fire := count == authRejectThreshold
	cb := h.onAuthReject
	h.authRejectMu.Unlock()
	if fire && cb != nil {
		cb(reason, count)
	}
}

// SetAuthRejectCallback installs the callback invoked when suspicious
// agent connections are detected. Call once during startup.
func (h *Hub) SetAuthRejectCallback(fn func(reason string, count int)) {
	h.authRejectMu.Lock()
	h.onAuthReject = fn
	h.authRejectMu.Unlock()
}

// SetFloodCallback installs the callback invoked when a connection flood is
// detected. Call once during startup.
func (h *Hub) SetFloodCallback(fn func(rejected int, detail string)) {
	h.floodMu.Lock()
	h.onFlood = fn
	h.floodMu.Unlock()
}

func NewHub(maxClients int, log *logger.Logger) *Hub {
	// Reserve the majority of the client slots for authenticated clients
	// (Agent + logged-in browsers) so anonymous viewers cannot exhaust them.
	// Keep a reasonable number of viewer slots for the public-viewer mode.
	maxViewers := maxClients / 4
	if maxViewers < 1 {
		maxViewers = 1
	}
	if maxViewers > 50 {
		maxViewers = 50
	}
	return &Hub{
		clients:    make(map[*wsClient]bool),
		maxClients: maxClients,
		maxViewers: maxViewers,
		log:        log,
		maxSamples: 3600, // ~1h at the Agent's 1s interval
	}
}

// SetCallbacks installs the agent callbacks under the hub lock. Any of the
// three may be nil to clear it.
func (h *Hub) SetCallbacks(
	onDisconnect func(),
	onStatus func(*status.SystemStatus),
	onEvent func([]byte),
) {
	h.Lock()
	h.onAgentDisconnect = onDisconnect
	h.onAgentStatus = onStatus
	h.onAgentEvent = onEvent
	h.Unlock()
}

// SetViewerMode enables public-viewer filtering: status broadcasts are
// sanitized and events are withheld for clients that are not authenticated,
// so an unauthenticated viewer cannot read the process list or disk S.M.A.R.T
// metadata through the live WebSocket feed. Call once during startup.
func (h *Hub) SetViewerMode(enabled bool) {
	h.Lock()
	h.viewerMode = enabled
	h.Unlock()
}

// sanitizeStatusJSON returns msg unchanged unless it is a status payload, in
// which case viewer-restricted fields are stripped. Non-status messages
// (agent commands / events) are returned as-is.
func sanitizeStatusJSON(msg []byte) []byte {
	var s status.SystemStatus
	if err := json.Unmarshal(msg, &s); err != nil || s.Timestamp <= 0 {
		return msg
	}
	out, err := json.Marshal(s.SanitizeForViewer())
	if err != nil {
		return msg
	}
	return out
}

// SetOnAgentEvent installs just the agent-event callback.
func (h *Hub) SetOnAgentEvent(fn func([]byte)) {
	h.Lock()
	h.onAgentEvent = fn
	h.Unlock()
}

// onDisconnectFn returns the disconnect callback under the read lock.
func (h *Hub) onDisconnectFn() func() {
	h.RLock()
	defer h.RUnlock()
	return h.onAgentDisconnect
}

// onEventFn returns the event callback under the read lock.
func (h *Hub) onEventFn() func([]byte) {
	h.RLock()
	defer h.RUnlock()
	return h.onAgentEvent
}

func (h *Hub) Broadcast(msg []byte) {
	h.RLock()
	viewerMode := h.viewerMode
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.RUnlock()

	// Compute the viewer-sanitized form once, not per client, so N public
	// viewers do not trigger N JSON unmarshal/marshal cycles each broadcast.
	// It is only needed when at least one unauthenticated viewer is connected;
	// with only authenticated clients (the common case) the unmarshal+marshal
	// per broadcast is skipped entirely.
	needSanitized := false
	if viewerMode {
		for _, c := range clients {
			if !c.authed {
				needSanitized = true
				break
			}
		}
	}
	var sanitized []byte
	if needSanitized {
		sanitized = sanitizeStatusJSON(msg)
	}

	for _, c := range clients {
		out := msg
		if viewerMode && !c.authed {
			out = sanitized
		}
		if err := c.writeMessage(websocket.TextMessage, out, 30*time.Second); err != nil {
			h.log.Debug("Broadcast 送信エラー: %v, クライアントを削除します", err)
			h.Remove(c)
		}
	}
}

// Add registers a WebSocket client. isAgent marks the single trusted Agent
// connection; authed marks a logged-in browser. Authenticated clients (Agent
// and logged-in users) are admitted up to maxClients. Unauthenticated clients
// are additionally capped at maxViewers so anonymous connections cannot
// exhaust the fixed client slots and deny service to the Agent or real users.
func (h *Hub) Add(conn *websocket.Conn, authed, isAgent bool) (*wsClient, bool) {
	h.Lock()
	defer h.Unlock()

	// Count unauthenticated viewers for the viewer-specific cap. The Agent and
	// authenticated browsers are not viewers even though they also occupy a
	// client slot.
	if !authed && !isAgent {
		viewers := 0
		for c := range h.clients {
			if !c.authed {
				viewers++
			}
		}
		if h.maxViewers > 0 && viewers >= h.maxViewers {
			h.log.Warn("未認証ビューアの上限 (%d) に達したため、接続を拒否しました", h.maxViewers)
			h.noteRejected("unauthenticated viewer cap")
			return nil, false
		}
	}

	if h.maxClients > 0 && len(h.clients) >= h.maxClients {
		// Authenticated clients take priority: if the slot is occupied by an
		// anonymous viewer, evict it so the Agent / a real user can connect.
		if authed || isAgent {
			if evicted := h.evictViewerLocked(); evicted {
				h.log.Warn("認証済みクライアントのため、未認証ビューアを1件切断しました (上限 %d)", h.maxClients)
			} else {
				h.log.Warn("最大接続数 (%d) に達したため、接続を拒否しました", h.maxClients)
				h.noteRejected("max clients reached")
				return nil, false
			}
		} else {
			h.log.Warn("最大接続数 (%d) に達したため、接続を拒否しました", h.maxClients)
			h.noteRejected("max clients reached")
			return nil, false
		}
	}

	client := &wsClient{conn: conn, authed: authed || isAgent}
	h.clients[client] = true
	h.log.Debug("クライアント追加: %s (現在 %d 接続)", conn.RemoteAddr(), len(h.clients))
	return client, true
}

// evictViewerLocked disconnects one unauthenticated viewer to free a slot for
// an authenticated client. Caller must hold h.Lock. It returns true when a
// viewer was evicted. The connection close is deferred to a goroutine so it
// does not run while holding the hub lock.
func (h *Hub) evictViewerLocked() bool {
	for c := range h.clients {
		if !c.authed {
			delete(h.clients, c)
			go c.conn.Close()
			return true
		}
	}
	return false
}

func (h *Hub) Remove(client *wsClient) {
	if client == nil {
		return
	}
	h.Lock()
	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		h.log.Debug("クライアント削除: %s (残り %d 接続)", client.conn.RemoteAddr(), len(h.clients))
	}
	wasAgent := (h.agentConn == client)
	if wasAgent {
		h.agentConn = nil
	}
	h.Unlock()

	// Invoke the callback after releasing the lock. The callback reaches into
	// the alert engine (and may, in the future, touch the hub again), so
	// calling it under the lock risks a deadlock. Read it via the locked
	// accessor so a concurrent SetCallbacks cannot race here.
	if wasAgent {
		if fn := h.onDisconnectFn(); fn != nil {
			fn()
		}
	}

	if err := client.conn.Close(); err != nil {
		h.log.Debug("クライアント Close エラー: %v", err)
	}
}

func (h *Hub) GetLastStatus() *status.SystemStatus {
	h.RLock()
	defer h.RUnlock()
	if h.lastStatus == nil {
		return nil
	}
	// Deep copy so callers cannot mutate the stored status (and its shared
	// slices/pointers) by accident.
	return h.lastStatus.Clone()
}

func (h *Hub) SetLastStatus(s *status.SystemStatus) {
	h.Lock()
	if s == nil {
		h.lastStatus = nil
		h.Unlock()
		return
	}
	// Reject out-of-range values so a buggy or hostile agent cannot pollute
	// the stored status, the metrics history, or the Prometheus endpoint
	// (e.g. cpu_usage=1e9 or negative percentages).
	if err := s.Validate(); err != nil {
		if h.log != nil {
			h.log.Warn("不正なステータスを破棄しました: %v", err)
		}
		h.Unlock()
		return
	}
	// Store a deep copy so the stored status does not share slices/pointers
	// with the caller's object.
	h.lastStatus = s.Clone()
	// Record a compact sample for the history API.
	h.samples = append(h.samples, api.HistorySample{
		Timestamp: s.Timestamp,
		CPU:       s.CPUUsage,
		Mem:       s.MemPercent,
		Disk:      s.DiskPercent,
	})
	if h.maxSamples > 0 && len(h.samples) > h.maxSamples {
		h.samples = h.samples[len(h.samples)-h.maxSamples:]
	}
	statusFn := h.onAgentStatus
	h.Unlock()

	if statusFn != nil {
		statusFn(s)
	}
}

// HistorySamples returns recent compact samples (oldest first).
func (h *Hub) HistorySamples() []api.HistorySample {
	h.RLock()
	defer h.RUnlock()
	out := make([]api.HistorySample, len(h.samples))
	copy(out, h.samples)
	return out
}

// MarkAgentConn registers client as the agent connection. It returns false
// when another agent connection is already registered, so a second client that
// presents the same token cannot displace the real agent. Without this, anyone
// who learns the agent token (e.g. via an admin session) could repeatedly
// connect and evict the real agent, flapping the monitoring feed.
func (h *Hub) MarkAgentConn(client *wsClient) bool {
	if client == nil {
		return false
	}
	h.Lock()
	defer h.Unlock()
	if h.agentConn != nil && h.agentConn != client {
		return false
	}
	h.agentConn = client
	return true
}

// AgentConnected reports whether an Agent is currently connected.
func (h *Hub) AgentConnected() bool {
	h.RLock()
	defer h.RUnlock()
	return h.agentConn != nil
}

// ★ Phase 8-5: Agent へメッセージを送信する
func (h *Hub) SendToAgent(payload map[string]interface{}) error {
	h.RLock()
	agentConn := h.agentConn
	h.RUnlock()

	if agentConn == nil {
		return fmt.Errorf("agent が接続されていません")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	return agentConn.writeMessage(websocket.TextMessage, data, 10*time.Second)
}

// ★ Phase 8-5: Agent 以外の全クライアント（ブラウザ）に中継する
func (h *Hub) BroadcastToBrowsers(msg []byte) {
	h.RLock()
	agentConn := h.agentConn
	viewerMode := h.viewerMode
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		if c == agentConn {
			continue
		}
		clients = append(clients, c)
	}
	h.RUnlock()

	for _, c := range clients {
		// Security events carry actor/IP data; do not relay them to
		// unauthenticated viewers.
		if viewerMode && !c.authed {
			continue
		}
		if err := c.writeMessage(websocket.TextMessage, msg, 30*time.Second); err != nil {
			h.log.Debug("BroadcastToBrowsers 送信エラー: %v", err)
			h.Remove(c)
		}
	}
}

// ============================================================
// main
// ============================================================
func main() {
	configPath := flag.String("config", "dashboard_config.json", "設定ファイルパス")
	flag.Parse()

	cfg, err := config.LoadDashboardConfig(*configPath)
	if err != nil {
		log.Fatalf("設定読み込み失敗: %v", err)
	}

	// Default to INFO so DEBUG noise is not emitted unless explicitly
	// enabled via dashboard_config.json's log_level ("debug").
	level := logger.INFO
	if cfg.LogLevel != "" {
		level = logger.ParseLevel(cfg.LogLevel)
	}

	lg := logger.NewLogger(&logger.Options{
		LogFile: cfg.LogFile,
		Level:   level,
		Prefix:  "[DASHBOARD]",
		UseUTC:  false,
	})
	defer lg.Sync()

	lg.Info("ダッシュボード起動 (listen: %s)", cfg.ListenAddr)
	if cfg.StaticDir != "" {
		lg.Info("静的ファイルディレクトリ: %s", cfg.StaticDir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		lg.Info("シャットダウンシグナル受信")
		cancel()
	}()

	notifierMgr := notify.FromConfig(cfg.Notifications, lg)
	if notifierMgr.HasChannels() {
		lg.Info("通知機能: 有効")
	} else {
		lg.Info("通知機能: 無効（チャンネル未設定）")
	}

	alertCfg := alert.Config{
		MemoryWarn:       cfg.Notifications.MemoryWarnPct,
		MemoryCritical:   cfg.Notifications.MemoryCriticalPct,
		DiskFreeWarn:     cfg.Notifications.DiskFreeWarnPct,
		DiskFreeCritical: cfg.Notifications.DiskFreeCriticalPct,
		CPUTempWarn:      cfg.Notifications.CPUTempWarnC,
		CPUTempCritical:  cfg.Notifications.CPUTempCriticalC,
		HoldDuration:     time.Duration(cfg.Notifications.HoldSec) * time.Second,
		RecoveryHold:     time.Duration(cfg.Notifications.RecoveryHoldSec) * time.Second,
		Cooldown:         time.Duration(cfg.Notifications.CooldownSec) * time.Second,
		AgentTimeout:     time.Duration(cfg.Notifications.AgentTimeoutSec) * time.Second,
		NotifyRecovery:   cfg.Notifications.NotifyRecovery,
	}
	engine := alert.NewEngine(alertCfg, notifierMgr, lg)

	// Persist alert history across restarts.
	historyFile := cfg.AlertHistoryFile
	if historyFile == "" {
		historyFile = "logs/alert_history.jsonl"
	}
	engine.EnableHistoryPersistence(historyFile)
	lg.Info("アラート履歴の永続化: %s", historyFile)

	// Resolve sibling config paths relative to the dashboard config, so a
	// custom -config path keeps the agent/modules configs in the same
	// directory instead of silently reading them from the current working
	// directory (which would edit a different file than the one loaded).
	configDir := filepath.Dir(*configPath)
	agentConfigPath := filepath.Join(configDir, "agent_config.json")
	modulesPath := filepath.Join(configDir, "modules.json")
	storage := api.NewModulesStorage(modulesPath)
	if err := storage.Load(); err != nil {
		lg.Warn("モジュール設定読み込みエラー: %v", err)
	}
	lg.Info("モジュール設定読み込み: %s (%d モジュール)", modulesPath, len(storage.GetAll()))

	hub := NewHub(100, lg)
	hub.agentToken = cfg.Auth.AgentToken
	// Public viewer filtering only matters when auth is enabled; with auth off
	// every client is trusted and full data is served as before.
	hub.SetViewerMode(cfg.Auth.Enabled && cfg.Auth.IsPublicViewer())
	hub.SetCallbacks(engine.OnAgentDisconnect, engine.OnStatus, nil)
	// Raise a security alert when a connection flood is detected, so an
	// operator sees the DoS on the dashboard and in the notification channels
	// instead of it being only a WARN line in the log.
	hub.SetFloodCallback(func(rejected int, detail string) {
		engine.ReportAlert(&notify.Alert{
			Type:      "connection_flood",
			Level:     notify.LevelCritical,
			Icon:      "🚨",
			Title:     "接続フラッドを検知",
			Message:   fmt.Sprintf("短時間に %d 件の接続を拒否しました（%s）。接続枯渇型のDoS攻撃の可能性があります。", rejected, detail),
			Timestamp: time.Now(),
		}, "dashboard", true)
		lg.Warn("接続フラッド検知: %d 件拒否 (%s)", rejected, detail)
	})
	// Raise a warning when suspicious agent connections (token mismatch
	// or a duplicate agent) are rejected, so brute-force / hijack attempts
	// reach the operator instead of only the log.
	hub.SetAuthRejectCallback(func(reason string, count int) {
		title := "不正なAgent接続を検知"
		switch reason {
		case "agent_token_mismatch":
			title = "Agentトークン不一致（偽装の可能性）"
		case "agent_duplicate":
			title = "2つ目のAgent接続（乗っ取りの可能性）"
		}
		engine.ReportAlert(&notify.Alert{
			Type:      reason,
			Level:     notify.LevelWarning,
			Icon:      "⚠️",
			Title:     title,
			Message:   fmt.Sprintf("短時間に %d 回の不正なAgent接続を拒否しました（%s）。", count, reason),
			Timestamp: time.Now(),
		}, "dashboard", true)
		lg.Warn("不正Agent接続検知: %s x%d", reason, count)
	})
	if cfg.Auth.AgentToken != "" {
		lg.Info("Agent トークン認証: 有効")
	}

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				engine.CheckAgentTimeout()
			}
		}
	}()

	// authHandler is created later in startup; the closures below reference
	// this variable so they can tell an authenticated browser from a public
	// viewer at request time.
	var authHandler *auth.Handler

	mux := http.NewServeMux()

	// ---- WebSocket handler ----
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// When an agent token is configured, an agent connection
		// (?role=agent) must present the matching token. Browsers never
		// send role=agent, so this cannot be used to bypass login.
		isAgentRole := r.URL.Query().Get("role") == "agent"
		if hub.agentToken != "" && isAgentRole {
			token := r.Header.Get("X-Kizuna-Agent-Token")
			if token == "" {
				token = r.URL.Query().Get("token")
			}
			if subtle.ConstantTimeCompare([]byte(token), []byte(hub.agentToken)) != 1 {
				http.Error(w, "invalid agent token", http.StatusUnauthorized)
				lg.Warn("Agent トークン不一致の接続を拒否: %s", r.RemoteAddr)
				hub.noteAuthReject("agent_token_mismatch")
				return
			}
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			lg.Error("WebSocket アップグレード失敗: %v", err)
			return
		}

		authed := authHandler != nil && authHandler.Authenticated(r)
		// The Agent authenticates with the shared token above (isAgentRole +
		// token match). Treat it as a trusted client so it is never counted
		// as an anonymous viewer and always wins a slot.
		isAgentConn := hub.agentToken != "" && isAgentRole
		client, ok := hub.Add(conn, authed, isAgentConn)
		if !ok {
			conn.WriteMessage(websocket.CloseMessage, []byte("max clients reached"))
			conn.Close()
			return
		}

		lg.Info("クライアント接続: %s", conn.RemoteAddr())

		// Cap incoming message size so a huge frame cannot exhaust memory.
		// A status message (processes/disks) is well under a few hundred KB.
		conn.SetReadLimit(8 << 20) // 8 MiB

		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(120 * time.Second))
			return nil
		})

		// Keep the connection alive. Browsers never send application data, so
		// without server-initiated pings the 120s read deadline would expire
		// and drop every browser tab roughly every 2 minutes.
		pingDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-pingDone:
					return
				case <-ticker.C:
					if err := client.writeControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
						return
					}
				}
			}
		}()

		go func() {
			defer close(pingDone)
			defer hub.Remove(client)
			// The agent is trusted immediately only when it authenticated
			// with the shared token. When no token is configured, fall back
			// to the legacy heuristic, but only for non-browser clients
			// (see eligibleForHeuristicPromotion).
			isAgent := hub.agentToken != "" && isAgentRole
			canPromoteHeuristically := eligibleForHeuristicPromotion(hub.agentToken, isAgentRole, r.Header.Get("Origin"))
			if isAgent {
				if !hub.MarkAgentConn(client) {
					lg.Warn("既にAgent接続があるため、2つ目のAgent接続を拒否: %s", conn.RemoteAddr())
					hub.noteAuthReject("agent_duplicate")
					conn.WriteMessage(websocket.CloseMessage, []byte("another agent is connected"))
					conn.Close()
					return
				}
				lg.Info("Agent 認証済み接続: %s", conn.RemoteAddr())
			}
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
						lg.Debug("読み取りエラー: %v", err)
					}
					break
				}

				conn.SetReadDeadline(time.Now().Add(120 * time.Second))

				// Determine the message type.
				var envelope struct {
					Event  string `json:"event"`
					Action string `json:"action"`
				}
				_ = json.Unmarshal(msg, &envelope)

				if envelope.Event != "" {
					// Only the Agent may originate events. A browser sending an
					// event-shaped message must not be able to forge notifications
					// (e.g. a fake security_alert) or alert-history entries.
					if !isAgent {
						lg.Debug("非Agentからのイベントを無視: %s", envelope.Event)
						continue
					}
					lg.Debug("イベント受信: %s", envelope.Event)
					if fn := hub.onEventFn(); fn != nil {
						fn(msg)
					}
					hub.BroadcastToBrowsers(msg)
					continue
				}

				if envelope.Action != "" {
					lg.Debug("コマンド受信（無視）: %s", envelope.Action)
					continue
				}

				// Treat as a status. A positive timestamp is required so
				// arbitrary JSON is not mistaken for a status.
				var s status.SystemStatus
				if err := json.Unmarshal(msg, &s); err != nil || s.Timestamp <= 0 {
					// Only the agent's payloads are relayed to browsers. A
					// non-agent must not be able to inject or rebroadcast
					// arbitrary data (e.g. a bogus "{}" that blanks every
					// other client's dashboard).
					if isAgent {
						hub.Broadcast(msg)
					}
					continue
				}
				// A status is accepted only from the agent. A non-agent
				// connection may become the agent only through the legacy
				// tokenless heuristic for non-browser clients. A plain
				// browser must not be able to overwrite the stored status
				// (spoofing metrics/history) or hijack the agent connection.
				accept, promote := acceptAgentPayload(isAgent, true, canPromoteHeuristically)
				if !accept {
					lg.Debug("非Agentからのステータスを無視: %s", conn.RemoteAddr())
					continue
				}
				if promote {
					if !hub.MarkAgentConn(client) {
						lg.Warn("既にAgent接続があるため、ヒューリスティック昇格を拒否: %s", conn.RemoteAddr())
						continue
					}
					isAgent = true
					lg.Info("Agent 識別: %s", conn.RemoteAddr())
				}
				hub.SetLastStatus(&s)
				hub.Broadcast(msg)
			}
		}()
	})

	// ---- REST API: latest status ----
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		s := hub.GetLastStatus()
		if s == nil {
			writeDashboardJSON(w, http.StatusNotFound, map[string]string{"error": "no status available"})
			return
		}
		// In public viewer mode an unauthenticated client must not see the
		// process list or disk S.M.A.R.T metadata, even though this endpoint
		// is otherwise reachable without a session.
		if cfg.Auth.Enabled && cfg.Auth.IsPublicViewer() && (authHandler == nil || !authHandler.Authenticated(r)) {
			s = s.SanitizeForViewer()
		}
		writeDashboardJSON(w, http.StatusOK, s)
	})

	// ---- Module management API ----
	moduleHandler := api.NewModuleHandler(storage, cfg.ResolvePluginsDir())
	moduleHandler.RegisterRoutes(mux)

	// ---- Plugin management API (passes hub) ----
	pluginManager, err := api.NewPluginManager(storage, cfg, lg, hub)
	if err != nil {
		lg.Error("PluginManager 初期化失敗: %v", err)
		lg.Warn("プラグイン API は無効化されます")
	} else {
		// Record plugin upload/delete (a code-execution boundary) in the
		// alert history and notifications, not only the log.
		pluginManager.SetAuditHook(func(kind, detail string) {
			engine.ReportAlert(&notify.Alert{
				Type:      "audit_" + kind,
				Level:     notify.LevelCritical,
				Icon:      "🚨",
				Title:     "プラグイン操作を検知",
				Message:   detail,
				Timestamp: time.Now(),
			}, "dashboard", true)
		})
		pluginManager.RegisterRoutes(mux)
		lg.Info("プラグイン API 有効")
	}

	// Handle agent events: release the plugin run guard AND turn security
	// events into notifications + alert history entries.
	hub.SetOnAgentEvent(func(msg []byte) {
		if pluginManager != nil {
			pluginManager.HandleAgentEvent(msg)
		}
		handleSecurityEvent(msg, engine, lg)
	})

	// ---- Metrics API ----
	metricHandler := api.NewMetricHandler(hub)
	metricHandler.RegisterRoutes(mux)
	lg.Info("メトリクス API 有効")

	// ---- Logs API ----
	// Resolve the agent log path from agent_config.json so the viewer
	// follows the actual configuration instead of a hardcoded default.
	agentLogPath := "logs/agent.log"
	if ac, err := config.LoadAgentConfig(agentConfigPath); err == nil && ac.LogFile != "" {
		agentLogPath = ac.LogFile
	}
	logHandler := api.NewLogHandler(
		agentLogPath, // agent log
		cfg.LogFile,  // dashboard log
		"logs",       // log directory
	)
	logHandler.RegisterRoutes(mux)
	lg.Info("ログ API 有効")

	// ---- Config API ----
	configHandler := api.NewConfigHandler(
		agentConfigPath,
		*configPath,
		modulesPath,
	)
	configHandler.RegisterRoutes(mux)
	lg.Info("設定 API 有効")

	// ---- Alert history API ----
	alertHandler := api.NewAlertHandler(engine)
	alertHandler.RegisterRoutes(mux)
	lg.Info("アラート履歴 API 有効")

	// ---- Version API ----
	versionHandler := api.NewVersionHandler()
	versionHandler.RegisterRoutes(mux)
	lg.Info("バージョン API 有効")

	// ---- Metrics history API ----
	historyHandler := api.NewHistoryHandler(hub)
	historyHandler.RegisterRoutes(mux)
	lg.Info("メトリクス履歴 API 有効")

	// ---- Alert threshold API ----
	alertCfgHandler := api.NewAlertConfigHandler(engine, cfg, *configPath, lg)
	alertCfgHandler.RegisterRoutes(mux)
	lg.Info("アラート設定 API 有効")

	// ---- Health check ----
	// Returns 503 when the Agent is not connected, so external monitors
	// (Uptime Kuma, etc.) can detect a degraded state.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		agentUp := hub.AgentConnected()
		status := http.StatusOK
		state := "healthy"
		if !agentUp {
			status = http.StatusServiceUnavailable
			state = "degraded"
		}
		writeDashboardJSON(w, status, map[string]interface{}{
			"status":          state,
			"agent_connected": agentUp,
			"timestamp":       time.Now().Format(time.RFC3339),
		})
	})

	// ---- Static files ----
	staticDir := cfg.StaticDir
	if staticDir == "" {
		staticDir = "./web/static"
	}
	fileServer := http.FileServer(http.Dir(staticDir))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Baseline security headers. The UI has no inline scripts and no
		// inline event handlers, so script-src can stay at 'self'. Inline
		// style attributes are used for gauge widths, so style-src needs
		// 'unsafe-inline'. Google Fonts is the only external origin.
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"img-src 'self' data:; "+
				"connect-src 'self' ws: wss:; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")

		// HTML, JS and CSS must be revalidated so UI/CSS edits are picked up
		// immediately. A long max-age on .css/.js meant a CSS fix stayed
		// invisible until the browser cache expired (24h) unless the version
		// query (?v=) changed, which is easy to forget. Revalidation is cheap
		// (304 when unchanged) and removes that footgun. Images/fonts keep the
		// long cache.
		p := r.URL.Path
		if strings.HasSuffix(p, ".html") || p == "/" ||
			strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") {
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		fileServer.ServeHTTP(w, r)
	}))

	// ---- Authentication and authorization ----
	// usersFile defaults to users.json next to the dashboard config, so a
	// custom -config path keeps the user store alongside it.
	usersFile := cfg.Auth.UsersFile
	if usersFile == "" {
		usersFile = filepath.Join(configDir, "users.json")
	}
	userStore := auth.NewStore(usersFile)
	if err := userStore.Load(); err != nil {
		lg.Error("ユーザーストア読み込み失敗: %v", err)
	}
	sessionTTL := time.Duration(cfg.Auth.SessionTTLHours) * time.Hour
	// Pass the configured idle timeout (0 = default 2h, negative = disabled)
	// so an operator can stop sessions expiring on short inactivity.
	sessionMgr := auth.NewSessionManager(sessionTTL, cfg.Auth.SessionIdleDuration())
	sessionMgr.SetLogger(lg.Warn)
	if err := sessionMgr.EnablePersistence(cfg.Auth.SessionFilePath(configDir), cfg.Auth.IsSessionIPBind()); err != nil {
		lg.Warn("セッション永続化の読み込みに失敗（メモリのみで続行）: %v", err)
	}
	if p := cfg.Auth.SessionFilePath(configDir); p != "" {
		lg.Info("セッション永続化: %s (IPバインド: %v)", p, cfg.Auth.IsSessionIPBind())
	} else {
		lg.Info("セッション永続化: 無効（再起動でログアウト）")
	}
	defer sessionMgr.Stop()
	authHandler = auth.NewHandler(userStore, sessionMgr, lg, cfg.Auth.SecureCookies, cfg.Auth.Enabled, cfg.Auth.AgentToken)
	defer authHandler.Stop()
	// Record security-relevant auth events (login lockouts = likely brute
	// force) in the alert history and notifications, not only the log.
	authHandler.SetSecurityEventHook(func(kind, detail, ip string) {
		title := "セキュリティ操作を検知"
		icon := "🔐"
		switch kind {
		case "login_lockout":
			title = "ログイン試行のロックアウト"
			icon = "🚨"
		case "user_create":
			title = "ユーザーを作成"
			icon = "👤"
		case "user_delete":
			title = "ユーザーを削除"
			icon = "🗑️"
		case "role_change":
			title = "ロールを変更"
			icon = "🔑"
		}
		msg := detail
		if ip != "" {
			msg += "（接続元: " + ip + "）"
		}
		engine.ReportAlert(&notify.Alert{
			Type:      "auth_" + kind,
			Level:     notify.LevelCritical,
			Icon:      icon,
			Title:     title,
			Message:   msg,
			Timestamp: time.Now(),
		}, "dashboard", true)
	})
	authHandler.SetAvatarsDir(filepath.Join(configDir, "avatars"))
	authHandler.SetGuestTTL(cfg.Auth.GuestSessionTTL())
	if cfg.Auth.IsPublicViewer() {
		authHandler.SetAllowGuest(true)
	}
	authHandler.RegisterRoutes(mux)
	authMiddleware := auth.NewMiddleware(authHandler, cfg.Auth.Enabled)
	if cfg.Auth.IsPublicViewer() {
		authMiddleware.SetPublicViewer(true)
		lg.Info("公開ビューアモード: 有効（ログイン不要で CPU/メモリ/ディスク使用率を閲覧可能）")
	}

	if cfg.Auth.Enabled {
		switch {
		case userStore.IsCorrupt():
			lg.Error("認証は有効ですが users.json が破損しています。管理者が手動で修正するまでログインできません: %s", usersFile)
		case userStore.NeedsSetup():
			lg.Warn("認証は有効ですがユーザーが未作成です。ブラウザで /setup.html にアクセスして管理者を作成してください")
		default:
			lg.Info("認証: 有効（ユーザー数: %d, users_file: %s）", userStore.Count(), usersFile)
		}
	} else {
		lg.Info("認証: 無効（dashboard_config.json の auth.enabled を true にすると有効化）")
	}

	// Warn when auth is on but no agent token is set: the agent cannot
	// authenticate, so it will never connect.
	if cfg.Auth.Enabled && cfg.Auth.AgentToken == "" {
		lg.Warn("認証は有効ですが auth.agent_token が未設定です。Agent は接続できません（agent_config.json の token と同じ値を設定してください）。")
	}

	// Warn loudly about a dangerous combination: plugin upload enabled while
	// authentication is off. Anyone on the network could upload and run a
	// .so, which is effectively remote code execution.
	if !cfg.Auth.Enabled && cfg.IsUploadEnabled() {
		lg.Warn("セキュリティ警告: plugins_upload_enabled=true かつ auth.enabled=false です。誰でもプラグイン(.so)を設置・実行できる状態（実質RCE）です。auth.enabled=true にするか plugins_upload_enabled=false にしてください。")
	}

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: bodyLimitMiddleware(authMiddleware.Wrap(mux)),
		// Bound how long the server waits for request headers and for an idle
		// keep-alive connection, so a slow-header (Slowloris) client cannot
		// hold connections open indefinitely. WriteTimeout is intentionally
		// NOT set: /api/logs/stream is a long-lived SSE response that a global
		// write deadline would sever. WebSocket connections hijack the conn
		// and are likewise unaffected by these values.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Cap request headers explicitly. Go's default is 1 MiB, which is far
		// more than this app's requests need (no large cookies / no huge
		// Authorization header); 64 KiB bounds memory per connection and makes
		// an oversized-header request fail fast with 431 instead of being read
		// into a 1 MiB buffer.
		MaxHeaderBytes: 64 << 10, // 64 KiB
	}

	go func() {
		<-ctx.Done()
		lg.Info("シャットダウンシグナル受信、サーバー停止中...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			lg.Error("サーバーシャットダウンエラー: %v", err)
		}
	}()

	lg.Info("サーバー開始: http://%s", cfg.ListenAddr)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		lg.Fatal("サーバー起動失敗: %v", err)
	}
	lg.Info("サーバー停止")
}
