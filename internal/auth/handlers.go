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
}

// NewHandler creates an auth Handler.
// secure should be true when the dashboard is served over HTTPS.
// authEnabled mirrors auth.enabled so the frontend can tell whether login is
// required at all (useful when auth is disabled: no setup/login redirects).
// agentToken is the shared secret the agent presents on /ws?role=agent.
func NewHandler(store *Store, manager *SessionManager, logger Logger, secure, authEnabled bool, agentToken string) *Handler {
	return &Handler{
		store:       store,
		manager:     manager,
		logger:      logger,
		secure:      secure,
		authEnabled: authEnabled,
		agentToken:  agentToken,
		limiter:     newLoginLimiter(10, 5*time.Minute, 5*time.Minute),
	}
}

// RegisterRoutes registers the auth routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", h.handleStatus)
	mux.HandleFunc("POST /api/auth/setup", h.handleSetup)
	mux.HandleFunc("POST /api/auth/login", h.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", h.handleLogout)
	mux.HandleFunc("GET /api/auth/me", h.handleMe)

	mux.HandleFunc("GET /api/users", h.requireAdmin(h.handleListUsers))
	mux.HandleFunc("POST /api/users", h.requireAdmin(h.handleCreateUser))
	mux.HandleFunc("DELETE /api/users/{name}", h.requireAdmin(h.handleDeleteUser))
	mux.HandleFunc("PUT /api/users/{name}/password", h.requireAdmin(h.handleChangePassword))
	mux.HandleFunc("PUT /api/users/{name}/role", h.requireAdmin(h.handleChangeRole))
}

// writeJSON writes JSON with a Content-Type header.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
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

// sessionFromRequest returns the current session, if any.
func (h *Handler) sessionFromRequest(r *http.Request) (*Session, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil, false
	}
	return h.manager.Get(c.Value)
}

// handleStatus reports whether setup is required and who is logged in.
// This endpoint is intentionally unauthenticated.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	needsSetup := h.store.NeedsSetup()
	resp := map[string]interface{}{
		"auth_enabled":  h.authEnabled,
		"needs_setup":   needsSetup && h.authEnabled,
		"authenticated": false,
	}
	if s, ok := h.sessionFromRequest(r); ok {
		resp["authenticated"] = true
		resp["username"] = s.Username
		resp["role"] = string(s.Role)
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
		writeError(w, http.StatusTooManyRequests, "試行回数が多すぎます。しばらく待ってから再度お試しください")
		return
	}

	u, ok := h.store.Authenticate(body.Username, body.Password)
	if !ok {
		if h.limiter != nil {
			h.limiter.RecordFailure(ip)
		}
		if h.logger != nil {
			h.logger.Warn("ログイン失敗: user=%s from=%s", body.Username, ip)
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

	sess, err := h.manager.Create(u.Username, u.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "セッションの作成に失敗しました")
		return
	}
	h.setCookie(w, sess.ID, sess.ExpiresAt)

	if h.logger != nil {
		h.logger.Info("ログイン成功: user=%s role=%s from=%s", u.Username, u.Role, r.RemoteAddr)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"username": u.Username,
		"role":     string(u.Role),
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"username": s.Username,
		"role":     string(s.Role),
	})
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
