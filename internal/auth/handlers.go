package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CookieName is the session cookie name.
const CookieName = "kizuna_session"

// Logger is the minimal logging interface used here.
type Logger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// sanitizeLogField neutralises untrusted text before it is written to a log.
//
// Two classes of injection are handled:
//
//  1. Line breaking / control characters. CR, LF and other C0/C1 controls
//     (NUL, ESC, ANSI introducers, ...) are DROPPED, not replaced with a
//     space. Replacing them with a space still leaves a readable forged
//     fragment such as "user=a [INFO] FORGED from=...", which a log parser
//     can mistake for a real entry.
//
//  2. Log-parser metacharacters. Brackets are the marker a parser (and a
//     human) uses to spot a level tag ([INFO], [WARN], ...). They are escaped
//     with a backslash so an injected "[INFO] FORGED" can never look like a
//     real log level.
//
// The value is only used for logging; authentication still uses the raw input.
func sanitizeLogField(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			// Drop the character entirely: a space would still separate a
			// forged "[INFO]" tag from the surrounding text.
			continue
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// Drop other control characters (NUL, ESC, ANSI introducers, ...).
			continue
		case r == '[' || r == ']':
			// Escape the bracket so an injected "[INFO]"/"[WARN]" tag cannot
			// be read as a real log level by a parser or by a human.
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Handler serves the auth API and login/setup pages.
type Handler struct {
	store       *Store
	manager     *SessionManager
	logger      Logger
	secure      bool // set Secure on cookies (HTTPS)
	authEnabled bool
	agentToken  string
	setupMu     sync.Mutex
	limiter     *loginLimiter
	// guestLimiter throttles guest-login creation per IP. Guest login is
	// unauthenticated (when public_viewer is on), so without a limit anyone
	// could flood it to grow the session store.
	guestLimiter *loginLimiter
	// avatarsDir holds uploaded avatar images (default: "avatars").
	avatarsDir string
	// allowGuest enables the "login as guest" button, which grants a
	// read-only viewer session without credentials. It is tied to
	// auth.public_viewer so the guest entry point only exists when the
	// operator explicitly opted in to login-free viewer access.
	allowGuest bool
	// guestTTL is the absolute lifetime of a guest session. Guests are
	// anonymous viewers, so their sessions expire far sooner than a real
	// user's (default 12h) to keep the session store small and to limit the
	// value of a stolen guest cookie.
	guestTTL time.Duration
	// onSecurityEvent, when set, receives security-relevant auth events (a
	// login lockout = likely brute force). The auth package has no dependency
	// on the alert engine, so the dashboard injects a callback that records
	// the event in the alert history and notifications. Without this, a
	// brute-force lockout was only a WARN line in the log.
	onSecurityEvent func(kind, detail, ip string)
}

// SetSecurityEventHook installs the callback invoked for security-relevant
// auth events (e.g. a login lockout). Call once during startup.
func (h *Handler) SetSecurityEventHook(fn func(kind, detail, ip string)) {
	h.onSecurityEvent = fn
}

// reportSecurityEvent invokes the hook, if any.
func (h *Handler) reportSecurityEvent(kind, detail, ip string) {
	if h.onSecurityEvent != nil {
		h.onSecurityEvent(kind, detail, ip)
	}
}

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

// SetAvatarsDir sets the directory used for avatar images.
func (h *Handler) SetAvatarsDir(dir string) { h.avatarsDir = dir }

// SetGuestTTL sets the absolute lifetime of a guest session. A value <= 0
// keeps the manager's default TTL.
func (h *Handler) SetGuestTTL(ttl time.Duration) { h.guestTTL = ttl }

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

// SetAllowGuest enables the guest login endpoint (viewer role, no password).
func (h *Handler) SetAllowGuest(enabled bool) { h.allowGuest = enabled }

// RegisterRoutes registers the auth routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", h.handleStatus)
	mux.HandleFunc("POST /api/auth/setup", h.handleSetup)
	mux.HandleFunc("POST /api/auth/login", h.handleLogin)
	mux.HandleFunc("POST /api/auth/guest", h.handleGuestLogin)
	mux.HandleFunc("POST /api/auth/logout", h.handleLogout)
	mux.HandleFunc("GET /api/auth/me", h.handleMe)

	// Self-service account management.
	mux.HandleFunc("PUT /api/auth/password", h.handleChangeOwnPassword)

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

// setCookie issues the session cookie.
func (h *Handler) setCookie(w http.ResponseWriter, id string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

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

// Authenticated reports whether the request carries a valid session. It is
// used by the dashboard to decide whether a public-viewer response must be
// sanitized (the middleware itself does not inject the session).
func (h *Handler) Authenticated(r *http.Request) bool {
	_, ok := h.sessionFromRequest(r)
	return ok
}

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

// handleSetup creates the first admin. Allowed only when auth is enabled and
// no user exists.
func (h *Handler) handleSetup(w http.ResponseWriter, r *http.Request) {
	// Serialize setup attempts so two concurrent requests cannot both pass
	// the NeedsSetup check and create two admins.
	h.setupMu.Lock()
	defer h.setupMu.Unlock()

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

	if _, err := h.store.Create(body.Username, body.Password, RoleAdmin); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.logger != nil {
		h.logger.Info("初回セットアップ完了: 管理者 '%s' を作成しました", body.Username)
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

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
			h.logger.Warn("ログイン試行がロック中: ip=%s", ip)
		}
		// A lockout means the per-IP failure threshold was reached: report it
		// as a security event so it lands in the alert history / notifications,
		// not just the log.
		h.reportSecurityEvent("login_lockout", "ログイン試行がロックアウトされました", ip)
		writeError(w, http.StatusTooManyRequests, "試行回数が多すぎます。しばらく待ってから再度お試しください")
		return
	}

	u, ok := h.store.Authenticate(body.Username, body.Password)
	if !ok {
		if h.limiter != nil {
			h.limiter.RecordFailure(ip)
		}
		if h.logger != nil {
			h.logger.Warn("ログイン失敗: user=%s from=%s", sanitizeLogField(body.Username), ip)
		}
		writeError(w, http.StatusUnauthorized, "ユーザー名またはパスワードが違います")
		return
	}
	if h.limiter != nil {
		h.limiter.Reset(ip)
	}

	// Session fixation defense: drop any existing session first.
	if old, ok := h.sessionFromRequest(r); ok {
		h.manager.Delete(old.ID)
	}

	sess, err := h.manager.Create(u.Username, u.Role, clientIP(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "セッションの作成に失敗しました")
		return
	}
	h.setCookie(w, sess.ID, sess.ExpiresAt)

	if h.logger != nil {
		h.logger.Info("ログイン成功: user=%s role=%s from=%s", sanitizeLogField(u.Username), u.Role, r.RemoteAddr)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"username": u.Username,
		"role":     string(u.Role),
	})
}

// handleGuestLogin starts a read-only viewer session without credentials.
// It is available only when auth.public_viewer is enabled; otherwise it
// behaves like a missing endpoint (404-style forbidden).
func (h *Handler) handleGuestLogin(w http.ResponseWriter, r *http.Request) {
	if !h.allowGuest {
		writeError(w, http.StatusForbidden, "ゲストログインは無効です")
		return
	}

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
	h.setCookie(w, sess.ID, sess.ExpiresAt)

	if h.logger != nil {
		h.logger.Info("ゲストログイン: role=viewer from=%s", clientIP(r))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"username": "guest",
		"role":     string(RoleViewer),
	})
}

// handleLogout ends the session.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s, ok := h.sessionFromRequest(r); ok {
		h.manager.Delete(s.ID)
	}
	h.clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

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
	// Verify the current password before changing.
	if _, ok := h.store.Authenticate(s.Username, body.CurrentPassword); !ok {
		writeError(w, http.StatusForbidden, "現在のパスワードが違います")
		return
	}
	if err := h.store.ChangePassword(s.Username, body.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Force re-login everywhere.
	h.manager.DeleteUserSessions(s.Username)
	h.clearCookie(w)
	if h.logger != nil {
		h.logger.Info("パスワード変更: user=%s", s.Username)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

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
		h.logger.Info("ユーザー作成: %s (role=%s)", body.Username, role)
	}
	// Audit: a new account (especially an admin) is a privilege change and
	// must be recorded, not only logged.
	h.reportSecurityEvent("user_create", "ユーザー '"+body.Username+"' を作成しました (role="+string(role)+")", clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

func (h *Handler) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "ユーザー名が指定されていません")
		return
	}

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
		h.logger.Info("ユーザー削除: %s", name)
	}
	h.reportSecurityEvent("user_delete", "ユーザー '"+name+"' を削除しました", clientIP(r))
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
	h.reportSecurityEvent("role_change", "ユーザー '"+name+"' のロールを '"+body.Role+"' に変更しました", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
