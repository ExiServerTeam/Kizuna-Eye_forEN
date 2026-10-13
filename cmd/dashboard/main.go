package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"Kizuna-Eye/internal/api"
	"Kizuna-Eye/pkg/alert"
	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/notify"
	"Kizuna-Eye/pkg/status"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// ============================================================
// Hub は WebSocket クライアントを管理する
// ============================================================
type Hub struct {
	sync.RWMutex
	clients    map[*websocket.Conn]bool
	maxClients int
	log        *logger.Logger
	lastStatus *status.SystemStatus

	// Phase 7: Agent 死活監視用
	agentConn         *websocket.Conn
	onAgentDisconnect func()
	onAgentStatus     func(*status.SystemStatus)
}

func NewHub(maxClients int, log *logger.Logger) *Hub {
	return &Hub{
		clients:    make(map[*websocket.Conn]bool),
		maxClients: maxClients,
		log:        log,
	}
}

func (h *Hub) Broadcast(msg []byte) {
	h.RLock()
	clients := make([]*websocket.Conn, 0, len(h.clients))
	for conn := range h.clients {
		clients = append(clients, conn)
	}
	h.RUnlock()

	for _, conn := range clients {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			h.log.Debug("Broadcast 送信エラー: %v, クライアントを削除します", err)
			h.Remove(conn)
		}
	}
}

func (h *Hub) Add(conn *websocket.Conn) bool {
	h.Lock()
	defer h.Unlock()
	if h.maxClients > 0 && len(h.clients) >= h.maxClients {
		h.log.Warn("最大接続数 (%d) に達したため、接続を拒否しました", h.maxClients)
		return false
	}
	h.clients[conn] = true
	h.log.Debug("クライアント追加: %s (現在 %d 接続)", conn.RemoteAddr(), len(h.clients))
	return true
}

// ============================================================
// Remove は クライアントを削除する
//
// 修正点: onAgentDisconnect() を h.Unlock() の前に呼ぶことで、
// Agent 再起動時のレースコンディション（復帰通知の遅延）を防ぐ
// ============================================================
func (h *Hub) Remove(conn *websocket.Conn) {
	h.Lock()
	if _, ok := h.clients[conn]; ok {
		delete(h.clients, conn)
		h.log.Debug("クライアント削除: %s (残り %d 接続)", conn.RemoteAddr(), len(h.clients))
	}
	wasAgent := (h.agentConn == conn)
	if wasAgent {
		h.agentConn = nil
		// ロック内で切断コールバックを呼ぶ（新Agentの接続より先に agentOnline=false を確定させる）
		if h.onAgentDisconnect != nil {
			h.onAgentDisconnect()
		}
	}
	h.Unlock()

	if err := conn.Close(); err != nil {
		h.log.Debug("クライアント Close エラー: %v", err)
	}
}

func (h *Hub) GetLastStatus() *status.SystemStatus {
	h.RLock()
	defer h.RUnlock()
	if h.lastStatus == nil {
		return nil
	}
	copied := *h.lastStatus
	return &copied
}

func (h *Hub) SetLastStatus(s *status.SystemStatus) {
	h.Lock()
	copied := *s
	h.lastStatus = &copied
	h.Unlock()

	if h.onAgentStatus != nil {
		h.onAgentStatus(s)
	}
}

func (h *Hub) MarkAgentConn(conn *websocket.Conn) {
	h.Lock()
	h.agentConn = conn
	h.Unlock()
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

	lg := logger.NewLogger(&logger.Options{
		LogFile: cfg.LogFile,
		Level:   logger.DEBUG,
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

	// ---------- Phase 7: 通知マネージャ ----------
	notifierMgr := notify.FromConfig(cfg.Notifications, lg)
	if notifierMgr.HasChannels() {
		lg.Info("通知機能: 有効")
	} else {
		lg.Info("通知機能: 無効（チャンネル未設定）")
	}

	// ---------- Phase 7: アラートエンジン ----------
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

	// モジュールストレージ
	modulesPath := "./modules.json"
	storage := api.NewModulesStorage(modulesPath)
	if err := storage.Load(); err != nil {
		lg.Warn("モジュール設定読み込みエラー: %v", err)
	}
	lg.Info("モジュール設定読み込み: %s (%d モジュール)", modulesPath, len(storage.GetAll()))

	hub := NewHub(100, lg)
	hub.onAgentStatus = engine.OnStatus
	hub.onAgentDisconnect = engine.OnAgentDisconnect

	// ---------- Phase 7: Agent 応答なしの定期チェック ----------
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

	// ---------- ルーティング ----------
	mux := http.NewServeMux()

	// ---- WebSocket ハンドラ ----
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			lg.Error("WebSocket アップグレード失敗: %v", err)
			return
		}

		if !hub.Add(conn) {
			conn.WriteMessage(websocket.CloseMessage, []byte("max clients reached"))
			conn.Close()
			return
		}

		lg.Info("クライアント接続: %s", conn.RemoteAddr())

		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(120 * time.Second))
			return nil
		})

		go func() {
			defer hub.Remove(conn)
			isAgent := false
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
						lg.Debug("読み取りエラー: %v", err)
					}
					break
				}

				// ★ 修正点: メッセージ受信成功時に ReadDeadline をリセット
				//   Agent は1秒ごとにメッセージを送るので、これで切断されなくなる
				conn.SetReadDeadline(time.Now().Add(120 * time.Second))

				var s status.SystemStatus
				if err := json.Unmarshal(msg, &s); err == nil {
					if !isAgent {
						isAgent = true
						hub.MarkAgentConn(conn)
						lg.Info("Agent 識別: %s", conn.RemoteAddr())
					}
					hub.SetLastStatus(&s)
				}
				hub.Broadcast(msg)
			}
		}()
	})

	// ---- REST API: 最新ステータス取得 ----
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s := hub.GetLastStatus()
		if s == nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no status available"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(s)
	})

	// ---- モジュール管理API ----
	moduleHandler := api.NewModuleHandler(storage)
	moduleHandler.RegisterRoutes(mux)

	// ---- プラグイン管理API ----
	// NewPluginManager は (manager, error) を返す新シグネチャに変更された。
	pluginManager, err := api.NewPluginManager(storage, lg)
	if err != nil {
		lg.Error("PluginManager 初期化失敗: %v", err)
		lg.Warn("プラグイン API は無効化されます")
	} else {
		pluginManager.RegisterRoutes(mux)
		lg.Info("プラグイン API 有効")
	}

	// ---- ヘルスチェック ----
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy","timestamp":"` + time.Now().Format(time.RFC3339) + `"}`))
	})

	// ---- 静的ファイル ----
	staticDir := cfg.StaticDir
	if staticDir == "" {
		staticDir = "./web/static"
	}
	mux.Handle("/", http.FileServer(http.Dir(staticDir)))

	// ---- HTTP サーバー ----
	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
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
