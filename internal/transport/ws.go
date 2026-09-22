package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"Kizuna-Eye/pkg/status"
)

// WebSocketTransport はWebSocketベースのトランスポートを実装する
type WebSocketTransport struct {
	mu     sync.RWMutex
	logger Logger

	// エージェント側（クライアント）用
	serverURL string
	conn      *websocket.Conn
	dialer    *websocket.Dialer

	// ダッシュボード側（サーバー）用
	listenAddr string
	upgrader   websocket.Upgrader
	server     *http.Server
	running    bool
	lastStatus *status.SystemStatus

	// 共通設定
	timeout      time.Duration
	pingInterval time.Duration
	readTimeout  time.Duration
}

// WSOption は WebSocketTransport のオプション
type WSOption func(*WebSocketTransport)

// WithWSTimeout はWebSocketタイムアウトを設定する
func WithWSTimeout(timeout time.Duration) WSOption {
	return func(w *WebSocketTransport) {
		w.timeout = timeout
	}
}

// WithWSPingInterval はPing間隔を設定する（デフォルト: 20秒）
func WithWSPingInterval(interval time.Duration) WSOption {
	return func(w *WebSocketTransport) {
		w.pingInterval = interval
	}
}

// WithWSReadTimeout はReadDeadlineを設定する（デフォルト: 30秒）
func WithWSReadTimeout(timeout time.Duration) WSOption {
	return func(w *WebSocketTransport) {
		w.readTimeout = timeout
	}
}

// WithWSLogger はロガーを設定する
func WithWSLogger(logger Logger) WSOption {
	return func(w *WebSocketTransport) {
		w.logger = logger
	}
}

// NewWSClient はエージェント用のWebSocketクライアントを作成する
func NewWSClient(serverURL string, opts ...WSOption) *WebSocketTransport {
	w := &WebSocketTransport{
		serverURL:    serverURL,
		timeout:      10 * time.Second,
		pingInterval: 20 * time.Second,
		readTimeout:  30 * time.Second,
	}
	for _, opt := range opts {
		opt(w)
	}
	if w.dialer == nil {
		w.dialer = &websocket.Dialer{
			HandshakeTimeout: w.timeout,
		}
	}
	return w
}

// NewWSServer はダッシュボード用のWebSocketサーバーを作成する
func NewWSServer(listenAddr string, opts ...WSOption) *WebSocketTransport {
	w := &WebSocketTransport{
		listenAddr:   listenAddr,
		timeout:      10 * time.Second,
		pingInterval: 20 * time.Second,
		readTimeout:  30 * time.Second,
		upgrader: websocket.Upgrader{
			CheckOrigin:     func(r *http.Request) bool { return true },
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// ---------- エージェント側（Sender）実装 ----------

// Send はWebSocketでステータスを送信する
func (w *WebSocketTransport) Send(ctx context.Context, s *status.SystemStatus) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.conn == nil {
		if err := w.connectLocked(ctx); err != nil {
			return fmt.Errorf("接続確立失敗: %w", err)
		}
	}

	// 接続が生きているかチェック
	if err := w.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
		w.log("接続切断検知、再接続します")
		w.conn.Close()
		w.conn = nil
		if err := w.connectLocked(ctx); err != nil {
			return fmt.Errorf("再接続失敗: %w", err)
		}
	}

	data, err := json.Marshal(s)
	if err != nil {
		return err
	}

	w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := w.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		// 【改善3】エラーログを追加
		w.log("WriteMessage エラー: %v", err)
		w.conn.Close()
		w.conn = nil
		return fmt.Errorf("書き込み失敗: %w", err)
	}
	return nil
}

// connectLocked はロック取得済みの状態で接続を確立する
// 【改善1】DialContext を使用して ctx を伝播させる
func (w *WebSocketTransport) connectLocked(ctx context.Context) error {
	// DialContext で接続（ctx キャンセルに対応）
	conn, _, err := w.dialer.DialContext(ctx, w.serverURL, nil)
	if err != nil {
		return err
	}
	w.conn = conn
	w.log("WebSocket接続確立: %s", w.serverURL)

	// 接続直後にReadDeadlineを初期設定
	conn.SetReadDeadline(time.Now().Add(w.readTimeout))

	// Pongハンドラの設定
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(w.readTimeout))
		return nil
	})

	// Ping送信ゴルーチン（ctx連携済み）
	go w.pingLoop(ctx)

	return nil
}

// pingLoop は定期的にPingを送信するゴルーチン
func (w *WebSocketTransport) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(w.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.log("PingLoop 停止")
			return
		case <-ticker.C:
			w.mu.RLock()
			conn := w.conn
			w.mu.RUnlock()

			if conn == nil {
				return
			}
			if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
				w.log("Ping送信失敗: %v", err)
				return
			}
		}
	}
}

// ---------- ダッシュボード側（Receiver）実装 ----------

// Receive はメモリ上の最新ステータスを返す
func (w *WebSocketTransport) Receive(ctx context.Context) (*status.SystemStatus, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if w.lastStatus == nil {
		return nil, nil
	}
	copied := *w.lastStatus
	return &copied, nil
}

// StartServer はWebSocketサーバーを起動する
func (w *WebSocketTransport) StartServer(ctx context.Context) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return fmt.Errorf("サーバーは既に起動しています")
	}
	w.running = true
	w.mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", w.handleWebSocket)
	mux.HandleFunc("/health", w.handleHealth)

	w.mu.Lock()
	w.server = &http.Server{
		Addr:    w.listenAddr,
		Handler: mux,
	}
	w.mu.Unlock()

	w.log("WebSocketサーバー起動: %s (エンドポイント: /ws)", w.listenAddr)

	go func() {
		<-ctx.Done()
		w.log("シャットダウンシグナル受信")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := w.server.Shutdown(shutdownCtx); err != nil {
			w.log("サーバーシャットダウンエラー: %v", err)
		}
	}()

	if err := w.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// handleWebSocket はWebSocket接続を受け付けるハンドラ
func (w *WebSocketTransport) handleWebSocket(resp http.ResponseWriter, req *http.Request) {
	conn, err := w.upgrader.Upgrade(resp, req, nil)
	if err != nil {
		w.log("WebSocketアップグレード失敗: %v", err)
		return
	}
	defer conn.Close()

	w.log("WebSocketクライアント接続: %s", conn.RemoteAddr())

	// サーバー側にもReadDeadlineを設定
	conn.SetReadDeadline(time.Now().Add(w.readTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(w.readTimeout))
		return nil
	})

	// 読み取りループ
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			// 【改善4】CloseAbnormalClosure を追加
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseAbnormalClosure) {
				w.log("読み取りエラー: %v", err)
			}
			break
		}

		var s status.SystemStatus
		if err := json.Unmarshal(msg, &s); err != nil {
			w.log("JSONパースエラー: %v", err)
			continue
		}

		w.mu.Lock()
		copied := s
		w.lastStatus = &copied
		w.mu.Unlock()

		w.log("ステータス受信: CPU=%.1f%%, Mem=%.1f%%, Disk=%.1f%%",
			s.CPUUsage, s.MemPercent, s.DiskPercent)
	}
}

// handleHealth はヘルスチェック用ハンドラ
func (w *WebSocketTransport) handleHealth(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(http.StatusOK)
	resp.Write([]byte(`{"status":"healthy"}`))
}

// StopServer はWebSocketサーバーを停止する
func (w *WebSocketTransport) StopServer() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.server == nil || !w.running {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.server.Shutdown(ctx); err != nil {
		return err
	}
	w.running = false
	return nil
}

// Close はリソースを解放する
func (w *WebSocketTransport) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.conn != nil {
		w.conn.Close()
		w.conn = nil
	}
	return w.StopServer()
}

// log はロガーが設定されていれば出力する
func (w *WebSocketTransport) log(format string, v ...interface{}) {
	if w.logger != nil {
		w.logger.Printf("[WSTransport] "+format, v...)
	}
}

// コンパイル時のインターフェース適合確認
var (
	_ Sender   = (*WebSocketTransport)(nil)
	_ Receiver = (*WebSocketTransport)(nil)
	_ Closer   = (*WebSocketTransport)(nil)
)
