package auth

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	kelog "Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/logsafe"
)

// CookieName はセッション cookie の名前だ。
// CookieName is the session cookie name.
const CookieName = "kizuna_session"

// Logger はここで使う最小のログ出力インターフェースだ。
// Logger is the minimal logging interface used here.
type Logger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// (sanitizeLogField は Kizuna-Eye/pkg/logsafe へ移動した。auth ハンドラと
// dashboard がドリフトした 2 つのコピーを保っていたため、細工されたプラグイン
// イベントが dashboard.log にレベルタグを偽造できた。logsafe.Field 参照。)
// (sanitizeLogField moved to Kizuna-Eye/pkg/logsafe: the auth handler and the
// dashboard kept two copies that had drifted apart, so a crafted plugin event
// could still forge a level tag in dashboard.log. See logsafe.Field.)

// Handler は auth API とログイン/セットアップページを提供する。
// Handler serves the auth API and login/setup pages.
type Handler struct {
	store       *Store
	manager     *SessionManager
	logger      Logger
	secure      bool // cookie に Secure を設定（HTTPS） / set Secure on cookies (HTTPS)
	authEnabled bool
	agentToken  string
	setupMu     sync.Mutex
	limiter     *loginLimiter
	// guestLimiter は IP ごとにゲストログイン生成を抑制する。ゲストログインは
	// 未認証なので（public_viewer がオンのとき）、制限が無いと誰でも洪水させて
	// セッションストアを成長させられる。
	// guestLimiter throttles guest-login creation per IP. Guest login is
	// unauthenticated (when public_viewer is on), so without a limit anyone
	// could flood it to grow the session store.
	guestLimiter *loginLimiter
	// avatarsDir はアップロードされたアバター画像を保持する（既定: "avatars"）。
	// avatarsDir holds uploaded avatar images (default: "avatars").
	avatarsDir string
	// allowGuest は「ゲストとしてログイン」ボタンを有効化する。これは認証情報
	// 無しで読み取り専用 viewer セッションを与える。auth.public_viewer に紐付け、
	// 運用者がログイン不要 viewer アクセスへ明示的にオプトインしたときのみ
	// ゲスト入口が存在するようにする。
	// allowGuest enables the "login as guest" button, which grants a
	// read-only viewer session without credentials. It is tied to
	// auth.public_viewer so the guest entry point only exists when the
	// operator explicitly opted in to login-free viewer access.
	allowGuest bool
	// guestTTL はゲストセッションの絶対寿命だ。ゲストは匿名 viewer なので、
	// そのセッションは実ユーザーよりはるかに早く失効し（既定 12h）、セッション
	// ストアを小さく保ち、盗まれたゲスト cookie の価値を限定する。
	// guestTTL is the absolute lifetime of a guest session. Guests are
	// anonymous viewers, so their sessions expire far sooner than a real
	// user's (default 12h) to keep the session store small and to limit the
	// value of a stolen guest cookie.
	guestTTL time.Duration
	// onSecurityEvent を設定すると、セキュリティ関連の auth イベント（ログイン
	// ロックアウト = ブルートフォースの可能性）を受け取る。auth パッケージは
	// アラートエンジンに依存しないため、dashboard がイベントをアラート履歴と
	// 通知に記録するコールバックを注入する。これが無いと、ブルートフォースの
	// ロックアウトはログの WARN 行でしかなかった。
	// onSecurityEvent, when set, receives security-relevant auth events (a
	// login lockout = likely brute force). The auth package has no dependency
	// on the alert engine, so the dashboard injects a callback that records
	// the event in the alert history and notifications. Without this, a
	// brute-force lockout was only a WARN line in the log.
	onSecurityEvent func(kind, detail, detailEN, ip string)
}

// SetSecurityEventHook はセキュリティ関連の auth イベント（例: ログイン
// ロックアウト）で呼ばれるコールバックを設定する。detail は日本語レンダリング、
// detailEN は英語だ（アラート履歴が両方を保存する）。起動中に 1 回呼ぶ。
// SetSecurityEventHook installs the callback invoked for security-relevant
// auth events (e.g. a login lockout). detail is the Japanese rendering,
// detailEN the English one (the alert history stores both). Call once during
// startup.
func (h *Handler) SetSecurityEventHook(fn func(kind, detail, detailEN, ip string)) {
	h.onSecurityEvent = fn
}

// reportSecurityEvent はフックがあれば呼ぶ。
// reportSecurityEvent invokes the hook, if any.
func (h *Handler) reportSecurityEvent(kind, detail, detailEN, ip string) {
	if h.onSecurityEvent != nil {
		h.onSecurityEvent(kind, detail, detailEN, ip)
	}
}

// NewHandler は auth Handler を作る。
// secure は dashboard が HTTPS で配信されるとき true にすべきだ。
// authEnabled は auth.enabled を映し、フロントエンドがログインがそもそも必要かを
// 判断できるようにする（auth 無効時に有用: setup/login リダイレクトが無い）。
// agentToken は agent が /ws?role=agent で提示する共有秘密だ。
// NewHandler creates an auth Handler.
// secure should be true when the dashboard is served over HTTPS.
// authEnabled mirrors auth.enabled so the frontend can tell whether login is
// required at all (useful when auth is disabled: no setup/login redirects).
// agentToken is the shared secret the agent presents on /ws?role=agent.
func NewHandler(store *Store, manager *SessionManager, logger Logger, secure, authEnabled bool, agentToken string) *Handler {
	return &Handler{
		store:        store,
		manager:      manager,
		logger:       logger,
		secure:       secure,
		authEnabled:  authEnabled,
		agentToken:   agentToken,
		limiter:      newLoginLimiter(10, 5*time.Minute, 5*time.Minute),
		guestLimiter: newLoginLimiter(30, 10*time.Minute, 10*time.Minute),
	}
}

// SetAvatarsDir はアバター画像に使うディレクトリを設定する。
// SetAvatarsDir sets the directory used for avatar images.
func (h *Handler) SetAvatarsDir(dir string) { h.avatarsDir = dir }

// SetGuestTTL はゲストセッションの絶対寿命を設定する。0 以下の値はマネージャの
// 既定 TTL を保つ。
// SetGuestTTL sets the absolute lifetime of a guest session. A value <= 0
// keeps the manager's default TTL.
func (h *Handler) SetGuestTTL(ttl time.Duration) { h.guestTTL = ttl }

// Stop はハンドラが所有するバックグラウンドゴルーチン（ログイン/ゲスト
// レート制限の reaper）を終了する。シャットダウン時に呼ぶ。
// Stop terminates background goroutines owned by the handler (login/guest
// rate-limiter reapers). Call on shutdown.
func (h *Handler) Stop() {
	if h.limiter != nil {
		h.limiter.Stop()
	}
	if h.guestLimiter != nil {
		h.guestLimiter.Stop()
	}
}

// SetAllowGuest はゲストログインエンドポイント（viewer ロール、パスワード無し）を
// 有効化する。
// SetAllowGuest enables the guest login endpoint (viewer role, no password).
func (h *Handler) SetAllowGuest(enabled bool) { h.allowGuest = enabled }

// RegisterRoutes は auth のルートを登録する。
// RegisterRoutes registers the auth routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", h.handleStatus)
	mux.HandleFunc("POST /api/auth/setup", h.handleSetup)
	mux.HandleFunc("POST /api/auth/login", h.handleLogin)
	mux.HandleFunc("POST /api/auth/guest", h.handleGuestLogin)
	mux.HandleFunc("POST /api/auth/logout", h.handleLogout)
	mux.HandleFunc("GET /api/auth/me", h.handleMe)

	// セルフサービスのアカウント管理。
	// Self-service account management.
	mux.HandleFunc("PUT /api/auth/password", h.handleChangeOwnPassword)

	// アバター: ログイン済みユーザーは自身のアバターを管理する。
	// Avatar: any logged-in user manages their own avatar.
	mux.HandleFunc("POST /api/auth/avatar", h.handleAvatarUpload)
	mux.HandleFunc("DELETE /api/auth/avatar", h.handleAvatarDelete)
	mux.HandleFunc("GET /api/auth/avatar/{name}", h.handleAvatarServe)

	mux.HandleFunc("GET /api/users", h.requireAdmin(h.handleListUsers))
	mux.HandleFunc("POST /api/users", h.requireAdmin(h.handleCreateUser))
	mux.HandleFunc("DELETE /api/users/{name}", h.requireAdmin(h.handleDeleteUser))
	mux.HandleFunc("PUT /api/users/{name}/password", h.requireAdmin(h.handleChangePassword))
	mux.HandleFunc("PUT /api/users/{name}/role", h.requireAdmin(h.handleChangeRole))
}

// writeJSON は Content-Type ヘッダ付きで JSON を書く。
// auth 応答は呼び出し元の識別情報/セッション状態を運ぶため、決してキャッシュ
// されてはならない。no-store が無いと、ブラウザがセッション無効化後（例: サーバー
// 再起動）に古い "authenticated:true" を返し、UI が既に存在しないログイン状態を
// 表示し得る。
// writeJSON writes JSON with a Content-Type header.
// Auth responses carry the caller's identity/session state, so they must never
// be cached. Without no-store a browser may serve a stale "authenticated:true"
// after the session was invalidated (e.g. server restart), making the UI show a
// logged-in state that no longer exists.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// secureRequest はリクエストが HTTPS で届いたかを返す。直接 TLS（r.TLS）か、
// 同一ホストのリバースプロキシが X-Forwarded-Proto: https を付けた場合だ。
// X-Forwarded-Proto はループバックのピアからのみ信頼する（リモートクライアントが
// 偽装するのを防ぐ）。Cookie の Secure 属性を HTTPS 配信時に自動で有効化し、
// 運用者が secure_cookies を設定し忘れても「HTTPS なのに Secure でない」事故を
// 防ぐ（多層防御）。
// secureRequest reports whether the request arrived over HTTPS: directly
// (r.TLS) or via a same-host reverse proxy that set X-Forwarded-Proto: https.
// X-Forwarded-Proto is honored only from a loopback peer (a remote client must
// not be able to spoof it). It auto-enables the cookie Secure attribute over
// HTTPS so a forgotten secure_cookies setting cannot leave a Secure-less
// cookie on an HTTPS site (defense in depth).
func secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !isLoopback(peer) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// setCookie はセッション cookie を発行する。Secure は設定値か「HTTPS で届いた
// こと」のいずれかで有効になる。
// setCookie issues the session cookie. Secure is on when the config says so or
// when the request arrived over HTTPS.
func (h *Handler) setCookie(w http.ResponseWriter, r *http.Request, id string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secure || secureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secure || secureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// sessionFromRequest は現在のセッション（あれば）を返す。IP 束縛が有効な場合、
// セッションはそれを生成した IP からのみ受け入れられる。
// sessionFromRequest returns the current session, if any. When IP binding is
// enabled, a session is only accepted from the IP that created it.
func (h *Handler) sessionFromRequest(r *http.Request) (*Session, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil, false
	}
	s, ok := h.manager.Get(c.Value)
	if !ok {
		return nil, false
	}
	if h.manager.IPBindEnabled() && s.IP != "" && s.IP != clientIP(r) {
		return nil, false
	}
	return s, true
}

// Authenticated はリクエストが有効なセッションを運ぶかを返す。dashboard が
// public-viewer 応答を無害化すべきか（ミドルウェア自体はセッションを注入しない）
// を決めるのに使う。
// Authenticated reports whether the request carries a valid session. It is
// used by the dashboard to decide whether a public-viewer response must be
// sanitized (the middleware itself does not inject the session).
func (h *Handler) Authenticated(r *http.Request) bool {
	_, ok := h.sessionFromRequest(r)
	return ok
}

// IsAdmin はリクエストが admin ロールの有効なセッションを運ぶかを返す。API 層へ
// 注入され（api.ModuleHandler.SetAdminChecker 参照）、パスベースのミドルウェア
// ルールが日常の設定編集と区別できない操作 — 特にコード実行境界であるモジュールの
// plugin_path 変更 — が依然 admin を要求できるようにする。
// IsAdmin reports whether the request carries a valid session with the admin
// role. It is injected into the API layer (see api.ModuleHandler.SetAdminChecker)
// so that operations the path-based middleware rule cannot distinguish from
// routine settings edits — notably changing a module's plugin_path, which is a
// code-execution boundary — can still require admin.
func (h *Handler) IsAdmin(r *http.Request) bool {
	s, ok := h.sessionFromRequest(r)
	return ok && s.Role == RoleAdmin
}

// handleStatus はセットアップが必要か、誰がログインしているかを報告する。
// このエンドポイントは意図的に未認証だ。
// handleStatus reports whether setup is required and who is logged in.
// This endpoint is intentionally unauthenticated.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	needsSetup := h.store.NeedsSetup()
	resp := map[string]interface{}{
		"auth_enabled":  h.authEnabled,
		"needs_setup":   needsSetup && h.authEnabled,
		"authenticated": false,
		"guest_enabled": h.allowGuest,
	}
	if s, ok := h.sessionFromRequest(r); ok {
		resp["authenticated"] = true
		resp["username"] = s.Username
		resp["role"] = string(s.Role)
		if av := h.store.AvatarOf(s.Username); av != "" {
			resp["avatar"] = av
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetup は最初の admin を作成する。auth が有効で、ユーザーが存在しない
// 場合のみ許可される。
// handleSetup creates the first admin. Allowed only when auth is enabled and
// no user exists.
func (h *Handler) handleSetup(w http.ResponseWriter, r *http.Request) {
	// セットアップ試行を直列化し、2 つの並行リクエストが両方 NeedsSetup チェックを
	// 通過して 2 人の admin を作成しないようにする。
	// Serialize setup attempts so two concurrent requests cannot both pass
	// the NeedsSetup check and create two admins.
	h.setupMu.Lock()
	defer h.setupMu.Unlock()

	// 認証が無効な間はセットアップを拒否する。そうしないと、ネットワーク上の
	// 誰でも admin アカウントを事前作成でき（users.json は既定で空）、
	// 後に運用者が auth を有効にした途端にそれが実 admin になる。これは
	// 時間差の権限昇格だ。
	// Refuse setup while authentication is disabled. Otherwise anyone on the
	// network could pre-create an admin account (users.json is empty by
	// default), which would then become a real admin as soon as the operator
	// later enables auth. That is a time-delayed privilege escalation.
	if !h.authEnabled {
		writeError(w, http.StatusForbidden, "認証が無効のためセットアップできません")
		return
	}

	if h.store.IsCorrupt() {
		writeError(w, http.StatusForbidden, "users.json が破損しているためセットアップできません。管理者が手動で修正してください")
		return
	}
	if !h.store.NeedsSetup() {
		writeError(w, http.StatusForbidden, "セットアップは既に完了しています")
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "リクエストの形式が不正です")
		return
	}

	u, err := h.store.Create(body.Username, body.Password, RoleAdmin)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 新しい admin をすぐにログインさせ、セットアップがユーザーを /login.html へ
	// 跳ね返して再認証させないようにする。セッション固定化防御: まず既存
	// セッションを落とす（handleLogin と同じ）。
	// Log the new admin in right away, so setup does not bounce the user to
	// /login.html and make them authenticate again. Session fixation defense:
	// drop any existing session first (same as handleLogin).
	if old, ok := h.sessionFromRequest(r); ok {
		h.manager.Delete(old.ID)
	}
	if sess, serr := h.manager.Create(u.Username, u.Role, clientIP(r)); serr == nil {
		h.setCookie(w, r, sess.ID, sess.ExpiresAt)
	} else if h.logger != nil {
		kelog.LogWarn(h.logger, "auth.setup_session_failed", serr.Error())
	}

	if h.logger != nil {
		kelog.LogInfo(h.logger, "auth.setup_done", body.Username)
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

// handleLogin は資格情報を検証し、セッションを開始する。
// handleLogin verifies credentials and starts a session.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "リクエストの形式が不正です")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	ip := clientIP(r)

	if h.limiter != nil && !h.limiter.Allow(ip) {
		if h.logger != nil {
			kelog.LogWarn(h.logger, "auth.login_locked", ip)
		}
		// ロックアウトは IP ごとの失敗閾値に達したことを意味する。ログだけでなく、
		// アラート履歴/通知に届くようセキュリティイベントとして報告する。
		// A lockout means the per-IP failure threshold was reached: report it
		// as a security event so it lands in the alert history / notifications,
		// not just the log.
		h.reportSecurityEvent("login_lockout", "ログイン試行がロックアウトされました", "Login attempts were locked out", ip)
		writeError(w, http.StatusTooManyRequests, "試行回数が多すぎます。しばらく待ってから再度お試しください")
		return
	}

	u, ok := h.store.Authenticate(body.Username, body.Password)
	if !ok {
		if h.limiter != nil {
			h.limiter.RecordFailure(ip)
		}
		if h.logger != nil {
			kelog.LogWarn(h.logger, "auth.login_failed", logsafe.Field(body.Username), ip)
		}
		writeError(w, http.StatusUnauthorized, "ユーザー名またはパスワードが違います")
		return
	}
	if h.limiter != nil {
		h.limiter.Reset(ip)
	}

	// セッション固定化防御: まず既存セッションを落とす。
	// Session fixation defense: drop any existing session first.
	if old, ok := h.sessionFromRequest(r); ok {
		h.manager.Delete(old.ID)
	}

	sess, err := h.manager.Create(u.Username, u.Role, clientIP(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "セッションの作成に失敗しました")
		return
	}
	h.setCookie(w, r, sess.ID, sess.ExpiresAt)

	if h.logger != nil {
		kelog.LogInfo(h.logger, "auth.login_success", logsafe.Field(u.Username), u.Role, r.RemoteAddr)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"username": u.Username,
		"role":     string(u.Role),
	})
}

// handleGuestLogin は資格情報無しで読み取り専用 viewer セッションを開始する。
// auth.public_viewer が有効なときのみ利用可能で、そうでなければ存在しない
// エンドポイントのように振る舞う（404 相当の forbidden）。
// handleGuestLogin starts a read-only viewer session without credentials.
// It is available only when auth.public_viewer is enabled; otherwise it
// behaves like a missing endpoint (404-style forbidden).
func (h *Handler) handleGuestLogin(w http.ResponseWriter, r *http.Request) {
	if !h.allowGuest {
		writeError(w, http.StatusForbidden, "ゲストログインは無効です")
		return
	}

	// 未認証エンドポイントがセッションストアを洪水させるのに使われないよう、
	// IP ごとに抑制する。
	// Throttle per IP so the unauthenticated endpoint cannot be used to flood
	// the session store.
	if h.guestLimiter != nil {
		ip := clientIP(r)
		if !h.guestLimiter.Allow(ip) {
			writeError(w, http.StatusTooManyRequests, "試行回数が多すぎます。しばらく待ってから再度お試しください")
			return
		}
		h.guestLimiter.Hit(ip)
	}

	// セッション固定化防御: まず既存セッションを落とす。
	// Session fixation defense: drop any existing session first.
	if old, ok := h.sessionFromRequest(r); ok {
		h.manager.Delete(old.ID)
	}

	sess, err := h.manager.CreateWithTTL("guest", RoleViewer, clientIP(r), h.guestTTL)
	if err != nil {
		if errors.Is(err, ErrTooManySessions) {
			writeError(w, http.StatusServiceUnavailable, "混雑しています。しばらく待ってから再度お試しください")
			return
		}
		writeError(w, http.StatusInternalServerError, "セッションの作成に失敗しました")
		return
	}
	h.setCookie(w, r, sess.ID, sess.ExpiresAt)

	if h.logger != nil {
		kelog.LogInfo(h.logger, "auth.guest_login", clientIP(r))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"username": "guest",
		"role":     string(RoleViewer),
	})
}

// handleLogout はセッションを終了する。
// handleLogout ends the session.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s, ok := h.sessionFromRequest(r); ok {
		h.manager.Delete(s.ID)
	}
	h.clearCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMe は現在のユーザーを返す。
// handleMe returns the current user.
func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "未ログインです")
		return
	}
	resp := map[string]interface{}{
		"username": s.Username,
		"role":     string(s.Role),
	}
	if av := h.store.AvatarOf(s.Username); av != "" {
		resp["avatar"] = av
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleChangeOwnPassword はログイン済みユーザーが自身のパスワードを変更できる
// ようにする。
// handleChangeOwnPassword lets a logged-in user change their own password.
func (h *Handler) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	s, ok := h.sessionFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "ログインが必要です")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "リクエストの形式が不正です")
		return
	}
	// 変更前に現在のパスワードを検証する。
	// Verify the current password before changing.
	if _, ok := h.store.Authenticate(s.Username, body.CurrentPassword); !ok {
		writeError(w, http.StatusForbidden, "現在のパスワードが違います")
		return
	}
	if err := h.store.ChangePassword(s.Username, body.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// どこでも再ログインを強制する。
	// Force re-login everywhere.
	h.manager.DeleteUserSessions(s.Username)
	h.clearCookie(w, r)
	if h.logger != nil {
		kelog.LogInfo(h.logger, "auth.password_changed", s.Username)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// requireAdmin はハンドラを包み、admin のみが呼べるようにする。
// requireAdmin wraps a handler so only admins may call it.
func (h *Handler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := h.sessionFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "未ログインです")
			return
		}
		if s.Role != RoleAdmin {
			writeError(w, http.StatusForbidden, "管理者権限が必要です")
			return
		}
		next(w, r)
	}
}

// ---------- ユーザー管理 ----------
// ---------- user management ----------

func (h *Handler) handleListUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"users": h.store.List()})
}

func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "リクエストの形式が不正です")
		return
	}

	role := Role(body.Role)
	if role == "" {
		role = RoleViewer
	}
	if _, err := h.store.Create(body.Username, body.Password, role); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrUserExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	if h.logger != nil {
		kelog.LogInfo(h.logger, "auth.user_created", body.Username, role)
	}
	// 監査: 新規アカウント（特に admin）は権限変更であり、ログだけでなく
	// 記録しなければならない。
	// Audit: a new account (especially an admin) is a privilege change and
	// must be recorded, not only logged.
	h.reportSecurityEvent("user_create", "ユーザー '"+body.Username+"' を作成しました (role="+string(role)+")", "Created user '"+body.Username+"' (role="+string(role)+")", clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

func (h *Handler) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "ユーザー名が指定されていません")
		return
	}

	// 自身のアカウントを削除するユーザーはリクエストの途中でセッションを失う。
	// 混乱を避けるため拒否する。
	// A user deleting their own account would drop their session mid-request;
	// refuse it to avoid confusion.
	if s, ok := h.sessionFromRequest(r); ok && s.Username == name {
		writeError(w, http.StatusBadRequest, "自分自身は削除できません")
		return
	}

	if err := h.store.Delete(name); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrUserNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, ErrLastAdmin) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	h.manager.DeleteUserSessions(name)
	if h.logger != nil {
		kelog.LogInfo(h.logger, "auth.user_deleted", name)
	}
	h.reportSecurityEvent("user_delete", "ユーザー '"+name+"' を削除しました", "Deleted user '"+name+"'", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "リクエストの形式が不正です")
		return
	}
	if err := h.store.ChangePassword(name, body.Password); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrUserNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	h.manager.DeleteUserSessions(name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleChangeRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "リクエストの形式が不正です")
		return
	}
	if err := h.store.ChangeRole(name, Role(body.Role)); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrUserNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, ErrLastAdmin) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	h.manager.DeleteUserSessions(name)
	h.reportSecurityEvent("role_change", "ユーザー '"+name+"' のロールを '"+body.Role+"' に変更しました", "Changed the role of user '"+name+"' to '"+body.Role+"'", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
