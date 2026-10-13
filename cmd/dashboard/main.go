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

// writeDashboardJSON writes JSON with a Content-Type header.
func writeDashboardJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
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
	})

	if lg != nil {
		lg.Info("セキュリティアラート: [%s] %s", env.Level, env.Title)
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
}

func NewHub(maxClients int, log *logger.Logger) *Hub {
	return &Hub{
		clients:    make(map[*wsClient]bool),
		maxClients: maxClients,
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
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.RUnlock()

	for _, c := range clients {
		if err := c.writeMessage(websocket.TextMessage, msg, 30*time.Second); err != nil {
			h.log.Debug("Broadcast 送信エラー: %v, クライアントを削除します", err)
			h.Remove(c)
		}
	}
}

func (h *Hub) Add(conn *websocket.Conn) (*wsClient, bool) {
	h.Lock()
	defer h.Unlock()
	if h.maxClients > 0 && len(h.clients) >= h.maxClients {
		h.log.Warn("最大接続数 (%d) に達したため、接続を拒否しました", h.maxClients)
		return nil, false
	}
	client := &wsClient{conn: conn}
	h.clients[client] = true
	h.log.Debug("クライアント追加: %s (現在 %d 接続)", conn.RemoteAddr(), len(h.clients))
	return client, true
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

func (h *Hub) MarkAgentConn(client *wsClient) {
	if client == nil {
		return
	}
	h.Lock()
	h.agentConn = client
	h.Unlock()
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
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		if c == agentConn {
			continue
		}
		clients = append(clients, c)
	}
	h.RUnlock()

	for _, c := range clients {
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

	level := logger.DEBUG
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
	hub.SetCallbacks(engine.OnAgentDisconnect, engine.OnStatus, nil)
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
				return
			}
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			lg.Error("WebSocket アップグレード失敗: %v", err)
			return
		}

		client, ok := hub.Add(conn)
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
				hub.MarkAgentConn(client)
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
					isAgent = true
					hub.MarkAgentConn(client)
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
		writeDashboardJSON(w, http.StatusOK, s)
	})

	// ---- Module management API ----
	moduleHandler := api.NewModuleHandler(storage)
	moduleHandler.RegisterRoutes(mux)

	// ---- Plugin management API (passes hub) ----
	pluginManager, err := api.NewPluginManager(storage, cfg, lg, hub)
	if err != nil {
		lg.Error("PluginManager 初期化失敗: %v", err)
		lg.Warn("プラグイン API は無効化されます")
	} else {
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

		// HTML must always be revalidated so UI changes are picked up immediately.
		// Versioned assets (?v=...) and other static files can be cached longer.
		if strings.HasSuffix(r.URL.Path, ".html") || r.URL.Path == "/" {
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
	sessionMgr := auth.NewSessionManager(sessionTTL, 0)
	defer sessionMgr.Stop()
	authHandler := auth.NewHandler(userStore, sessionMgr, lg, cfg.Auth.SecureCookies, cfg.Auth.Enabled, cfg.Auth.AgentToken)
	authHandler.RegisterRoutes(mux)
	authMiddleware := auth.NewMiddleware(authHandler, cfg.Auth.Enabled)

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
