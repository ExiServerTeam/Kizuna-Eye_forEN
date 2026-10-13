package auth

import (
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// routeRule declares the minimum role needed for a path prefix.
type routeRule struct {
	// method restricts the rule to a single HTTP method. An empty method
	// matches any method, so a path can have a viewer-readable GET and an
	// operator-only DELETE (see /api/alerts).
	method string
	prefix string
	min    Role
}

// protectedRules maps API path prefixes to the minimum role.
// More specific prefixes (and method-qualified rules) must be listed before
// broader ones; the first match wins. Order matters.
var protectedRules = []routeRule{
	// Destructive operations that share a path with a read-only endpoint
	// must come first. Clearing the alert history is state-changing and
	// destroys the audit trail, so it needs operator. A viewer is read-only
	// and must never be able to delete it.
	{"DELETE", "/api/alerts", RoleOperator},

	// Plugin upload and deletion are admin-only. Listing plugin metadata and
	// running a backup are needed by the modules page, which operators may
	// use, so those fall through to the operator rule below.
	{"", "/api/plugins/upload", RoleAdmin},
	{"DELETE", "/api/plugins", RoleAdmin},

	// Self-account writes are for real users only. A guest session is a
	// throw-away viewer that has no entry in users.json, so an avatar
	// upload/delete would fail internally (500). An operator-level rule
	// keeps guests out of these endpoints while still letting a logged-in
	// operator/admin manage their own avatar. Reads (GET
	// /api/auth/avatar/{name}) stay public for <img> tags.
	{"POST", "/api/auth/avatar", RoleOperator},
	{"DELETE", "/api/auth/avatar", RoleOperator},

	// Admin only: configuration management.
	{"", "/api/config", RoleAdmin},

	// Module registration and deletion are admin-only: adding a module means
	// supplying a plugin_path, which the agent will load as code (a .so), so it
	// is a code-execution boundary, not a routine operation. An operator may
	// still CHANGE an existing module's settings (PUT) and read the list (GET),
	// which the modules page needs.
	{"POST", "/api/modules", RoleAdmin},
	{"DELETE", "/api/modules", RoleAdmin},
	// Reload re-reads modules.json and applies new plugin paths; treat it like
	// a registration action and require admin.
	{"POST", "/api/modules/reload", RoleAdmin},

	// Operator: manual backup runs, plugin metadata/UI, module config edits,
	// and alert threshold changes.
	{"", "/api/plugins", RoleOperator},
	{"", "/api/alert-config", RoleOperator},
	{"", "/api/modules", RoleOperator},
	{"", "/plugins", RoleOperator},

	// Logs may contain usernames, IP addresses, file paths and internal
	// errors, so they are operator-and-above only. A read-only viewer must
	// not be able to read them.
	{"", "/api/logs", RoleOperator},

	// Viewer and above: read-only monitoring endpoints.
	{"", "/api/status", RoleViewer},
	{"", "/api/metrics", RoleViewer},
	{"", "/api/history", RoleViewer},
	{"", "/api/alerts", RoleViewer},
	{"", "/api/version", RoleViewer},
	{"", "/ws", RoleViewer},
}

// Middleware enforces authentication and authorization for browser traffic.
// It is applied to everything except a small allowlist (login, setup, static
// assets needed to render those pages, and health checks).
type Middleware struct {
	handler *Handler
	enabled bool

	// publicViewer allows unauthenticated access to the dashboard page,
	// /ws and /api/status so a viewer can see CPU/memory/disk usage without
	// logging in. All other endpoints still require a session.
	publicViewer bool
}

// NewMiddleware creates the auth middleware.
// When enabled is false, all requests pass through unchanged (backward compat).
func NewMiddleware(handler *Handler, enabled bool) *Middleware {
	return &Middleware{handler: handler, enabled: enabled}
}

// SetPublicViewer enables the login-free viewer mode. It must be called
// during startup before the server begins serving requests.
func (m *Middleware) SetPublicViewer(enabled bool) {
	m.publicViewer = enabled
}

// isPublicViewerPath reports whether a path may be served without a session
// when public viewer mode is on. Only the dashboard page, its static assets,
// the live WebSocket feed and the latest-status snapshot are exposed; history,
// alerts, logs, modules and user management stay behind login.
func isPublicViewerPath(method, p string) bool {
	// Live feed and initial snapshot (read-only, viewer-level data).
	if p == "/ws" {
		return true
	}
	if p == "/api/status" || strings.HasPrefix(p, "/api/status?") {
		return method == http.MethodGet || method == http.MethodHead
	}
	// The dashboard itself. Only the root page is public; modules,
	// config-editor, logs and users pages are not.
	if p == "/" || p == "/index.html" {
		return true
	}
	// Static assets needed to render the dashboard.
	switch p {
	case "/app.js", "/style.css", "/i18n.js", "/auth-guard.js",
		"/auth-theme.js", "/connection.js":
		return true
	}
	return false
}

// csrfOriginOK reports whether a state-changing request comes from this
// host. Safe methods (GET/HEAD/OPTIONS) are always allowed. A request with no
// Origin header is treated as a non-browser client (curl, a script) and
// allowed; browsers always send Origin on cross-site and same-origin
// POST/PUT/DELETE, so a mismatch means the request originated elsewhere.
// This complements SameSite=Lax cookies (defense in depth against CSRF).
func csrfOriginOK(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	originHost := canonicalHost(u.Host)
	// r.Host is the Host the browser sent, which the reverse proxy forwards.
	if originHost == canonicalHost(r.Host) {
		return true
	}
	// Behind a reverse proxy the Host may be rewritten, so the request can
	// instead carry X-Forwarded-Host. Honor it ONLY when the peer is a local
	// (loopback) proxy: an arbitrary remote client must not be able to spoof
	// X-Forwarded-Host and bypass the check. The request is accepted if Origin
	// matches either value, so a correct r.Host is never broken by a stray XFH.
	if peer, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && isLoopback(peer) {
		if xfh := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))); xfh != "" && originHost == canonicalHost(xfh) {
			return true
		}
	}
	return false
}

// canonicalHost normalizes a Host/Origin value for comparison. It lowercases
// the value, drops a trailing dot (FQDN form) and maps the loopback aliases
// localhost / 127.0.0.1 / [::1] to a single canonical form, so the same host
// reached via any of its usual spellings is treated as same-origin. Without
// this, a browser that reached the dashboard via http://127.0.0.1:8080 would
// have its state-changing requests rejected with 403 even though the Origin is
// the very host it is talking to.
func canonicalHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimSuffix(h, ".")
	// Split host:port so the loopback mapping does not depend on the port.
	hostPart, portPart := h, ""
	if hp, p, err := net.SplitHostPort(h); err == nil {
		hostPart, portPart = hp, p
	}
	switch hostPart {
	case "localhost", "127.0.0.1", "::1", "[::1]", "0:0:0:0:0:0:0:1":
		hostPart = "localhost"
	}
	if portPart != "" {
		return net.JoinHostPort(hostPart, portPart)
	}
	return hostPart
}

// isPluginUploadPath reports whether p is the plugin upload endpoint, which is
// a remote-code-execution vector (a .so runs with the server's privileges).
func isPluginUploadPath(p string) bool {
	return p == "/api/plugins/upload"
}

// isPublicPath reports whether a path may be served without a session.
func isPublicPath(p string) bool {
	// Avatar images are loaded by <img> tags; the file names carry an
	// unguessable random suffix. Only GET is served here anyway.
	if strings.HasPrefix(p, "/api/auth/avatar/") {
		return true
	}

	// Static assets (CSS / JS / fonts / images) are shared by every page,
	// including the login and setup screens which are shown before a session
	// exists. Serving them without a session is safe: they contain no user
	// data, and gating them caused the login page to receive a 302 instead of
	// its stylesheet (broken UI). Only .html pages stay gated (per-page rules
	// below), so an admin shell is never served to a lower role.
	switch {
	case strings.HasSuffix(p, ".css"),
		strings.HasSuffix(p, ".js"),
		strings.HasSuffix(p, ".woff"),
		strings.HasSuffix(p, ".woff2"),
		strings.HasSuffix(p, ".ttf"),
		strings.HasSuffix(p, ".png"),
		strings.HasSuffix(p, ".svg"),
		strings.HasSuffix(p, ".ico"),
		strings.HasSuffix(p, ".webp"):
		return true
	}

	switch p {
	case "/health",
		"/api/auth/status",
		"/api/auth/setup",
		"/api/auth/login",
		"/api/auth/guest",
		"/login.html",
		"/setup.html":
		return true
	}
	return false
}

// staticPageRules maps a static HTML page to the minimum role required to
// view it. The data APIs behind these pages are already protected, but gating
// the pages themselves removes the reliance on client-side redirects (defense
// in depth: a viewer or JS-disabled client cannot even load the admin shell).
var staticPageRules = map[string]Role{
	"/logs.html":          RoleOperator,
	"/modules.html":       RoleOperator,
	"/config-editor.html": RoleAdmin,
	"/users.html":         RoleAdmin,
}

// canonicalPath normalizes a request path before rule matching, so that an
// alias such as /api/plugins/x/../upload cannot match a weaker rule than the
// real path it resolves to (G-4). net/http's mux redirects the alias to this
// canonical form, so the checks and the finally-served route always agree.
func canonicalPath(p string) string {
	return path.Clean(p)
}

// minRoleFor returns the minimum role for an HTTP method and API path, and
// whether it is covered by a rule. Rules with a non-empty method only apply
// to that method. Paths without a rule fall through to viewer (any login).
func minRoleFor(method, p string) (Role, bool) {
	for _, r := range protectedRules {
		if r.method != "" && !strings.EqualFold(method, r.method) {
			continue
		}
		if p == r.prefix || strings.HasPrefix(p, r.prefix+"/") || strings.HasPrefix(p, r.prefix+"?") {
			return r.min, true
		}
	}
	return RoleViewer, false
}

// Wrap returns next wrapped with the authentication/authorization checks.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// G-4: パスを正規化してからルール照合する。/api/plugins/../config の
		// ような別名が本来より緩いルール（前方一致）にマッチして権限判定を
		// すり抜けるのを防ぐ。mux 側も正規形へリダイレクトするため、ここで
		// 判定するパスと最終的に処理されるパスが一致する。
		cleanPath := canonicalPath(r.URL.Path)

		// CSRF defense in depth, applied even when auth is off (harmless and
		// keeps the check in one place). SameSite=Lax already blocks the
		// session cookie on cross-site POSTs; verifying Origin closes the gap.
		if !csrfOriginOK(r) {
			writeError(w, http.StatusForbidden, "クロスサイトリクエストを拒否しました")
			return
		}

		if !m.enabled {
			// Auth is disabled (trusted-network mode), so routes are normally
			// open. Plugin upload is the exception: an unauthenticated .so
			// upload is remote code execution, so refuse it until auth is on.
			if isPluginUploadPath(cleanPath) {
				writeError(w, http.StatusForbidden, "プラグインのアップロードには認証が必要です（dashboard_config.json の auth.enabled を true にしてください）")
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		path := cleanPath

		// Agent WebSocket connections authenticate with a shared token, not
		// a session cookie. Let them through to the /ws handler, which
		// validates the token. Without this, enabling auth would block the
		// agent entirely.
		if path == "/ws" && r.URL.Query().Get("role") == "agent" && m.handler.agentToken != "" {
			next.ServeHTTP(w, r)
			return
		}

		// Setup flow: if no user exists, force the browser to /setup.html
		// (or allow the setup API), so the first admin can be created.
		if m.handler.store.NeedsSetup() {
			if isPublicPath(path) {
				next.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(path, "/api/") || path == "/ws" {
				writeError(w, http.StatusForbidden, "セットアップが必要です")
				return
			}
			http.Redirect(w, r, "/setup.html", http.StatusFound)
			return
		}

		// Public paths (login page, health, auth status) are always allowed.
		if isPublicPath(path) {
			next.ServeHTTP(w, r)
			return
		}

		// Login-free viewer mode: the dashboard page, its assets, /ws and
		// /api/status are served without a session. If a session IS present,
		// fall through so the normal role checks (and, for /ws, the agent
		// path) still apply.
		if m.publicViewer && isPublicViewerPath(r.Method, path) {
			if _, ok := m.handler.sessionFromRequest(r); !ok {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Everything else requires a valid session.
		sess, ok := m.handler.sessionFromRequest(r)
		if !ok {
			if strings.HasPrefix(path, "/api/") || path == "/ws" {
				writeError(w, http.StatusUnauthorized, "ログインが必要です")
				return
			}
			// Static pages: send the browser to the login page.
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}

		// For /api paths and plugin web UIs, enforce the minimum role.
		if strings.HasPrefix(path, "/api/") || path == "/ws" || strings.HasPrefix(path, "/plugins/") {
			min, _ := minRoleFor(r.Method, path)
			if !sess.Role.AtLeast(min) {
				writeError(w, http.StatusForbidden, "権限が不足しています")
				return
			}
		}

		// Gate role-restricted static pages on the server too, so the admin UI
		// shell is never served to a lower role (the frontend also redirects,
		// but that must not be the only barrier).
		if min, ok := staticPageRules[path]; ok {
			if !sess.Role.AtLeast(min) {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}
