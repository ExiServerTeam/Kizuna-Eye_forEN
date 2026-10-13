package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"Kizuna-Eye/pkg/status"
)

// HTTPTransport はHTTPベースのトランスポートを実装する
type HTTPTransport struct {
	// 共通設定
	client  *http.Client
	timeout time.Duration
	logger  Logger

	// エージェント側（送信）用
	serverURL string

	// ダッシュボード側（受信）用
	listenAddr  string
	handler     http.Handler
	enableAPI   bool // 【改善1】内部APIを有効にするか（デフォルト: true）
	logResponse bool // 【改善3】成功時のレスポンスをログに出力するか

	// 内部状態
	mu         sync.RWMutex
	server     *http.Server
	running    bool
	lastStatus *status.SystemStatus
}

// HTTPOption は HTTPTransport のオプション
type HTTPOption func(*HTTPTransport)

// WithHTTPTimeout はHTTPタイムアウトを設定する
func WithHTTPTimeout(timeout time.Duration) HTTPOption {
	return func(h *HTTPTransport) {
		h.timeout = timeout
		if h.client != nil {
			h.client.Timeout = timeout
		}
	}
}

// WithHTTPClient はカスタムHTTPクライアントを設定する
func WithHTTPClient(client *http.Client) HTTPOption {
	return func(h *HTTPTransport) {
		h.client = client
	}
}

// WithHTTPLogger はロガーを設定する
func WithHTTPLogger(logger Logger) HTTPOption {
	return func(h *HTTPTransport) {
		h.logger = logger
	}
}

// WithInternalAPI は内部API（/api/status, /health）の有効/無効を設定する
// 【改善1】デフォルトは true（有効）
func WithInternalAPI(enabled bool) HTTPOption {
	return func(h *HTTPTransport) {
		h.enableAPI = enabled
	}
}

// WithLogResponse は成功時のレスポンスログ出力を設定する
// 【改善3】デフォルトは false（出力しない）
func WithLogResponse(enabled bool) HTTPOption {
	return func(h *HTTPTransport) {
		h.logResponse = enabled
	}
}

// NewHTTPTransport はエージェント用のHTTPトランスポートを作成する（Sender）
func NewHTTPTransport(serverURL string, opts ...HTTPOption) *HTTPTransport {
	h := &HTTPTransport{
		serverURL:   serverURL,
		timeout:     10 * time.Second,
		enableAPI:   true, // エージェント側では意味がないが念のため
		logResponse: false,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// NewHTTPReceiver はダッシュボード用のHTTPトランスポートを作成する（Receiver）
func NewHTTPReceiver(listenAddr string, opts ...HTTPOption) *HTTPTransport {
	h := &HTTPTransport{
		listenAddr:  listenAddr,
		timeout:     10 * time.Second,
		enableAPI:   true,
		logResponse: false,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ---------- エージェント側（Sender）実装 ----------

// Send はHTTP POSTでステータスを送信する
func (h *HTTPTransport) Send(ctx context.Context, s *status.SystemStatus) error {
	h.mu.RLock()
	url := h.serverURL
	logResp := h.logResponse
	h.mu.RUnlock()

	if url == "" {
		return fmt.Errorf("serverURL が設定されていません")
	}

	data, err := json.Marshal(s)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("レスポンス読み取りエラー: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("HTTPエラー: status=%d, body=%s", resp.StatusCode, string(body))
	}

	// 【改善3】成功時のレスポンスログ（オプション）
	if logResp {
		h.log("送信成功: %s", string(body))
	}

	return nil
}

// ---------- ダッシュボード側（Receiver）実装 ----------

// Receive はメモリ上の最新ステータスを返す
// 【改善2】SystemStatus はポインタを含まない構造体のため、浅いコピーで十分
func (h *HTTPTransport) Receive(ctx context.Context) (*status.SystemStatus, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if h.lastStatus == nil {
		return nil, nil
	}

	// 浅いコピー（SystemStatus にポインタは含まれないため安全）
	copied := *h.lastStatus
	return &copied, nil
}

// StartServer はHTTPサーバーを起動する
func (h *HTTPTransport) StartServer(ctx context.Context) error {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return fmt.Errorf("サーバーは既に起動しています")
	}
	h.running = true

	enableAPI := h.enableAPI
	customHandler := h.handler
	h.mu.Unlock()

	mux := http.NewServeMux()

	// 【改善4】カスタムハンドラと内部APIの共存
	if customHandler != nil {
		if enableAPI {
			// 内部APIを優先するラッパー
			h.log("カスタムハンドラ + 内部API (/api/status, /health) を使用します")
			mux.Handle("/", h.wrapHandler(customHandler))
		} else {
			// カスタムハンドラに完全に委譲（内部API無効）
			h.log("カスタムハンドラのみを使用します（内部API無効）")
			mux.Handle("/", customHandler)
		}
	} else {
		// デフォルト: 内部APIのみ
		if enableAPI {
			mux.HandleFunc("/api/status", h.handleStatus)
			mux.HandleFunc("/health", h.handleHealth)
			h.log("内部API (/api/status, /health) を使用します")
		} else {
			// 内部API無効でハンドラなし → 404
			h.log("警告: ハンドラが設定されておらず、内部APIも無効です")
		}
	}

	// 内部APIが有効な場合、カスタムハンドラの有無に関わらずマウント
	// （カスタムハンドラが優先されるが、内部APIも別途マウント）
	if enableAPI {
		// カスタムハンドラが既に / を握っている場合でも、
		// 明示的に /api/status と /health をマウントする
		mux.HandleFunc("/api/status", h.handleStatus)
		mux.HandleFunc("/health", h.handleHealth)
	}

	h.mu.Lock()
	h.server = &http.Server{
		Addr:    h.listenAddr,
		Handler: mux,
	}
	h.mu.Unlock()

	h.log("HTTPサーバー起動: %s", h.listenAddr)

	go func() {
		<-ctx.Done()
		h.log("シャットダウンシグナル受信")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.server.Shutdown(shutdownCtx); err != nil {
			h.log("サーバーシャットダウンエラー: %v", err)
		}
	}()

	if err := h.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// wrapHandler はカスタムハンドラをラップし、内部APIパスを優先する
// 【改善4】内部APIパス（/api/status, /health）は内部ハンドラにルーティングし、
// それ以外はカスタムハンドラに委譲する
func (h *HTTPTransport) wrapHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 内部APIパスは常に内部ハンドラが処理
		if r.URL.Path == "/api/status" || r.URL.Path == "/health" {
			// 内部ハンドラにルーティングするために、再度 mux に委譲する必要がある
			// ここではシンプルに http.DefaultServeMux を使うか、直接ハンドラを呼ぶ
			// より良い方法: 内部ハンドラを直接呼び出す
			if r.URL.Path == "/api/status" {
				h.handleStatus(w, r)
			} else if r.URL.Path == "/health" {
				h.handleHealth(w, r)
			}
			return
		}
		// それ以外はカスタムハンドラ
		next.ServeHTTP(w, r)
	})
}

// handleStatus はステータスを受信するHTTPハンドラ
func (h *HTTPTransport) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("Content-Type") != "application/json" {
		h.log("不正なContent-Type: %s", r.Header.Get("Content-Type"))
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.log("リクエストボディ読み取りエラー: %v", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var s status.SystemStatus
	if err := json.Unmarshal(body, &s); err != nil {
		h.log("JSONパースエラー: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// メモリに保存（浅いコピー）
	h.mu.Lock()
	copied := s
	h.lastStatus = &copied
	h.mu.Unlock()

	h.log("ステータス受信: CPU=%.1f%%, Mem=%.1f%%, Disk=%.1f%%",
		s.CPUUsage, s.MemPercent, s.DiskPercent)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// handleHealth はヘルスチェック用ハンドラ
func (h *HTTPTransport) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
}

// StopServer はHTTPサーバーを停止する
func (h *HTTPTransport) StopServer() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.server == nil || !h.running {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.server.Shutdown(ctx); err != nil {
		return err
	}
	h.running = false
	return nil
}

// SetHandler はカスタムHTTPハンドラを設定する
// StartServer の前に呼び出すこと
func (h *HTTPTransport) SetHandler(handler http.Handler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handler = handler
}

// Close はリソースを解放する
func (h *HTTPTransport) Close() error {
	return h.StopServer()
}

// log はロガーが設定されていれば出力する
func (h *HTTPTransport) log(format string, v ...interface{}) {
	if h.logger != nil {
		h.logger.Printf("[HTTPTransport] "+format, v...)
	}
}

// コンパイル時のインターフェース適合確認
var (
	_ Sender   = (*HTTPTransport)(nil)
	_ Receiver = (*HTTPTransport)(nil)
	_ Closer   = (*HTTPTransport)(nil)
)
