package auth

import (
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// routeRule はパスプレフィックスに必要な最小ロールを宣言する。
// routeRule declares the minimum role needed for a path prefix.
type routeRule struct {
	// method はルールを単一の HTTP メソッドへ制限する。空の method は任意の
	// メソッドにマッチするため、1 つのパスが viewer 読み取り可能な GET と
	// operator 専用の DELETE を持ち得る（/api/alerts 参照）。
	// method restricts the rule to a single HTTP method. An empty method
	// matches any method, so a path can have a viewer-readable GET and an
	// operator-only DELETE (see /api/alerts).
	method string
	prefix string
	min    Role
}

// protectedRules は API パスプレフィックスを最小ロールへ対応付ける。
// より具体的なプレフィックス（およびメソッド修飾ルール）を広いものより先に
// 列挙しなければならない。最初の一致が勝つ。順序が重要だ。
// protectedRules maps API path prefixes to the minimum role.
// More specific prefixes (and method-qualified rules) must be listed before
// broader ones; the first match wins. Order matters.
var protectedRules = []routeRule{
	// ワンクリック対処（タスク2）は特権 sudo ヘルパー（kill / firewall /
	// cron 削除）を実行するため admin 専用だ。このルールは汎用の /api/alerts
	// viewer ルールより前に来なければならない。これが無い POST は viewer へ
	// フォールスルーし、読み取り専用ユーザーがシステム状態を変更できてしまう。
	// One-click remediation (task 2) runs a privileged sudo helper
	// (kill / firewall / cron delete), so it is admin-only. The rule must
	// precede the generic /api/alerts viewer rule; a POST without it would
	// fall through to viewer and let a read-only user change system state.
	{"POST", "/api/alerts", RoleAdmin},

	// 読み取り専用エンドポイントとパスを共有する破壊的操作は先に来なければ
	// ならない。アラート履歴のクリアは監査証跡（インシデント調査に必要かも
	// しれない）を破壊するため admin 専用だ。viewer/operator は履歴に対して
	// 読み取り専用で、決して削除できてはならない。
	// Destructive operations that share a path with a read-only endpoint
	// must come first. Clearing the alert history destroys the audit trail
	// (which may be needed to investigate an incident), so it is admin-only.
	// A viewer/operator is read-only for history and must never be able to
	// delete it.
	{"DELETE", "/api/alerts", RoleAdmin},

	// プラグインのアップロードと削除は admin 専用だ。プラグインメタデータの
	// 列挙とバックアップ実行は modules ページが必要とし、operator が使い得るため、
	// それらは下の operator ルールへフォールスルーする。
	// Plugin upload and deletion are admin-only. Listing plugin metadata and
	// running a backup are needed by the modules page, which operators may
	// use, so those fall through to the operator rule below.
	{"", "/api/plugins/upload", RoleAdmin},
	{"DELETE", "/api/plugins", RoleAdmin},

	// 自己アカウントの書き込みは実ユーザーのみだ。ゲストセッションは
	// users.json にエントリを持たない使い捨ての viewer なので、アバターの
	// アップロード/削除は内部的に失敗する（500）。operator レベルのルールが
	// ゲストをこれらのエンドポイントから締め出しつつ、ログイン済みの
	// operator/admin が自身のアバターを管理できるようにする。読み取り（GET
	// /api/auth/avatar/{name}）は <img> タグ用に公開のままにする。
	// Self-account writes are for real users only. A guest session is a
	// throw-away viewer that has no entry in users.json, so an avatar
	// upload/delete would fail internally (500). An operator-level rule
	// keeps guests out of these endpoints while still letting a logged-in
	// operator/admin manage their own avatar. Reads (GET
	// /api/auth/avatar/{name}) stay public for <img> tags.
	{"POST", "/api/auth/avatar", RoleOperator},
	{"DELETE", "/api/auth/avatar", RoleOperator},

	// admin 専用: 設定管理。
	// Admin only: configuration management.
	{"", "/api/config", RoleAdmin},

	// モジュールの登録と削除は admin 専用だ。モジュールの追加は plugin_path の
	// 供給を意味し、agent がそれをコード（.so）としてロードするため、
	// コード実行境界であり日常操作ではない。operator は既存モジュールの設定を
	// 変更（PUT）し一覧を読む（GET）ことは依然でき、modules ページが必要とする。
	// Module registration and deletion are admin-only: adding a module means
	// supplying a plugin_path, which the agent will load as code (a .so), so it
	// is a code-execution boundary, not a routine operation. An operator may
	// still CHANGE an existing module's settings (PUT) and read the list (GET),
	// which the modules page needs.
	{"POST", "/api/modules", RoleAdmin},
	{"DELETE", "/api/modules", RoleAdmin},
	// reload は modules.json を再読み込みし新しいプラグインパスを適用する。
	// 登録操作と同様に扱い admin を要求する。
	// Reload re-reads modules.json and applies new plugin paths; treat it like
	// a registration action and require admin.
	{"POST", "/api/modules/reload", RoleAdmin},

	// operator: 手動バックアップ実行、プラグインメタデータ/UI、モジュール設定編集、
	// アラート閾値変更。
	//
	// security insight はアラート履歴（viewer レベルのデータ）を集約するが、
	// 設定ポスチャも採点し、どの制御がオフかを明示する。それは配備の弱点の
	// 地図なので、未認証の公開 viewer が読めてはならない。operator 以上のみ。
	// Operator: manual backup runs, plugin metadata/UI, module config edits,
	// and alert threshold changes.
	//
	// The security insight aggregates the alert history (viewer-level data)
	// but also scores the configuration posture, which spells out which
	// controls are off. That is a map of the deployment's weaknesses, so it
	// must not be readable by an unauthenticated public viewer; operator and
	// above only.
	{"", "/api/insight", RoleOperator},
	{"", "/api/plugins", RoleOperator},
	{"", "/api/alert-config", RoleOperator},
	{"", "/api/modules", RoleOperator},
	{"", "/plugins", RoleOperator},

	// ログはユーザー名、IP アドレス、ファイルパス、内部エラーを含み得るため、
	// operator 以上のみだ。読み取り専用 viewer はそれらを読めてはならない。
	// Logs may contain usernames, IP addresses, file paths and internal
	// errors, so they are operator-and-above only. A read-only viewer must
	// not be able to read them.
	{"", "/api/logs", RoleOperator},

	// viewer 以上: 読み取り専用の監視エンドポイント。
	// Viewer and above: read-only monitoring endpoints.
	{"", "/api/status", RoleViewer},
	{"", "/api/metrics", RoleViewer},
	{"", "/api/history", RoleViewer},
	{"", "/api/alerts", RoleViewer},
	{"", "/api/version", RoleViewer},
	// モジュールのロード結果（loaded/failed/disabled）。dashboard はそれらを
	// モジュールカードに表示する。ルールが無ければ viewer 既定へフォールスルー
	// するが、ここで空ルールのケースを明示し、将来その既定が変わっても
	// ランタイム状態が viewer から黙って隠れないようにする。
	// Module load results (loaded/failed/disabled). The dashboard shows
	// them on the module card; without the rule they would fall through to
	// the viewer default, but the empty-rule case is explicit here so a
	// future change to that default does not silently hide the runtime
	// state from viewers.
	{"", "/api/plugin-runtime", RoleViewer},
	{"", "/ws", RoleViewer},
}

// Middleware はブラウザトラフィックの認証と認可を強制する。小さな許可リスト
// （ログイン、セットアップ、それらのページを描画するのに必要な静的アセット、
// ヘルスチェック）を除くすべてに適用される。
// Middleware enforces authentication and authorization for browser traffic.
// It is applied to everything except a small allowlist (login, setup, static
// assets needed to render those pages, and health checks).
type Middleware struct {
	handler *Handler
	enabled bool

	// publicViewer は dashboard ページ、/ws、/api/status への未認証アクセスを
	// 許可し、viewer がログインせずに CPU/メモリ/ディスク使用率を見られるようにする。
	// 他のすべてのエンドポイントは依然セッションを要する。
	// publicViewer allows unauthenticated access to the dashboard page,
	// /ws and /api/status so a viewer can see CPU/memory/disk usage without
	// logging in. All other endpoints still require a session.
	publicViewer bool
}

// NewMiddleware は認証ミドルウェアを作る。
// enabled が false のとき、すべてのリクエストは変更なく通過する（後方互換）。
// NewMiddleware creates the auth middleware.
// When enabled is false, all requests pass through unchanged (backward compat).
func NewMiddleware(handler *Handler, enabled bool) *Middleware {
	return &Middleware{handler: handler, enabled: enabled}
}

// SetPublicViewer はログイン不要の viewer モードを有効化する。サーバーが
// リクエストを処理し始める前の起動中に呼ばなければならない。
// SetPublicViewer enables the login-free viewer mode. It must be called
// during startup before the server begins serving requests.
func (m *Middleware) SetPublicViewer(enabled bool) {
	m.publicViewer = enabled
}

// isPublicViewerPath は、public viewer モードが有効なときにセッション無しで
// 配信してよいパスかを返す。dashboard ページ、その静的アセット、ライブ WebSocket
// フィード、最新 status スナップショットのみを公開し、履歴・アラート・ログ・
// モジュール・ユーザー管理はログインの背後に留める。
// isPublicViewerPath reports whether a path may be served without a session
// when public viewer mode is on. Only the dashboard page, its static assets,
// the live WebSocket feed and the latest-status snapshot are exposed; history,
// alerts, logs, modules and user management stay behind login.
func isPublicViewerPath(method, p string) bool {
	// ライブフィードと初期スナップショット（読み取り専用、viewer レベルのデータ）。
	// Live feed and initial snapshot (read-only, viewer-level data).
	if p == "/ws" {
		return true
	}
	if p == "/api/status" || strings.HasPrefix(p, "/api/status?") {
		return method == http.MethodGet || method == http.MethodHead
	}
	// dashboard 自体。ルートページのみ公開で、modules、config-editor、
	// logs、users ページは公開しない。
	// The dashboard itself. Only the root page is public; modules,
	// config-editor, logs and users pages are not.
	if p == "/" || p == "/index.html" {
		return true
	}
	// dashboard の描画に必要な静的アセット。
	// Static assets needed to render the dashboard.
	switch p {
	case "/app.js", "/style.css", "/i18n.js", "/auth-guard.js",
		"/auth-theme.js", "/connection.js":
		return true
	}
	return false
}

// csrfOriginOK は状態変更リクエストがこのホストから来たかを返す。安全なメソッド
// （GET/HEAD/OPTIONS）は常に許可する。Origin ヘッダの無いリクエストは非ブラウザ
// クライアント（curl、スクリプト）として扱い許可する。ブラウザはクロスサイト・
// 同一オリジンの POST/PUT/DELETE で常に Origin を送るため、不一致はリクエストが
// 他所から発したことを意味する。これは SameSite=Lax cookie を補完する
// （CSRF への多層防御）。
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
		return true // 非ブラウザクライアント / non-browser client
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	originHost := canonicalHost(u.Host)
	// r.Host はブラウザが送った Host で、リバースプロキシが転送する。
	// r.Host is the Host the browser sent, which the reverse proxy forwards.
	if originHost == canonicalHost(r.Host) {
		return true
	}
	// リバースプロキシ背後では Host が書き換えられ得るため、リクエストは代わりに
	// X-Forwarded-Host を運び得る。これはピアがローカル（ループバック）プロキシの
	// ときのみ尊重する。任意のリモートクライアントが X-Forwarded-Host を偽装して
	// チェックを迂回できてはならない。Origin がいずれかの値に一致すればリクエストを
	// 受け入れるため、正しい r.Host がはぐれ XFH で壊れることもない。
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

// canonicalHost は比較用に Host/Origin 値を正規化する。値を小文字化し、
// 末尾のドット（FQDN 形式）を落とし、ループバック別名 localhost / 127.0.0.1 /
// [::1] を単一の正規形へマップする。これにより、通常の綴りのいずれかで到達した
// 同じホストが同一オリジンとして扱われる。これが無いと、http://127.0.0.1:8080 で
// dashboard に到達したブラウザの状態変更リクエストが 403 で拒否されてしまう。
// オリジンがまさに通信相手のホストであるにもかかわらずだ。
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
	// host:port を分割し、ループバックのマップがポートに依存しないようにする。
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

// isPluginUploadPath は p がプラグインアップロードエンドポイントかを返す。
// これはリモートコード実行の経路だ（.so はサーバーの権限で実行される）。
// isPluginUploadPath reports whether p is the plugin upload endpoint, which is
// a remote-code-execution vector (a .so runs with the server's privileges).
func isPluginUploadPath(p string) bool {
	return p == "/api/plugins/upload"
}

// isPublicPath はセッション無しで配信してよいパスかを返す。
// isPublicPath reports whether a path may be served without a session.
func isPublicPath(p string) bool {
	// アバター画像は <img> タグがロードし、ファイル名は推測不能なランダム
	// 接尾辞を運ぶ。ここでは GET のみが配信される。
	// Avatar images are loaded by <img> tags; the file names carry an
	// unguessable random suffix. Only GET is served here anyway.
	if strings.HasPrefix(p, "/api/auth/avatar/") {
		return true
	}

	// プラグイン同梱の Web UI（/plugins/{name}/...）は operator 専用だ
	// （/plugins ルール下で handleWebUI が配信する）。ファイルが .js/.css などで
	// 終わるというだけで下の静的アセットルールをすり抜けてはならない。
	// そうしないと /plugins/{name}/app.js がセッション無しで配信され、
	// プラグインの UI（および埋め込むデータ）を未認証クライアントへ漏らす。
	// false を返し、通常のロールチェックを適用させる。
	// A plugin's bundled web UI (/plugins/{name}/...) is operator-only (it is
	// served by handleWebUI under the /plugins rule). It must NOT slip through
	// the static-asset rule below just because a file ends in .js/.css/etc.:
	// otherwise /plugins/{name}/app.js would be served without a session,
	// leaking a plugin's UI (and any data it embeds) to unauthenticated
	// clients. Return false so the normal role check applies.
	if strings.HasPrefix(p, "/plugins/") {
		return false
	}

	// 静的アセット（CSS / JS / フォント / 画像）は、セッションが存在する前に
	// 表示されるログイン・セットアップ画面を含め、すべてのページが共有する。
	// セッション無しで配信しても安全だ。ユーザーデータを含まず、ゲートすると
	// ログインページがスタイルシートではなく 302 を受け取り（UI が壊れる）。
	// .html ページのみゲートしたままにする（下のページ単位ルール）ため、
	// admin シェルが低いロールへ配信されることは決してない。
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
		strings.HasSuffix(p, ".otf"),
		strings.HasSuffix(p, ".eot"),
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

// staticPageRules は静的 HTML ページを、それを表示するのに必要な最小ロールへ
// 対応付ける。これらのページ背後のデータ API は既に保護されているが、ページ自体を
// ゲートすることでクライアント側リダイレクトへの依存を除く（多層防御: viewer や
// JS 無効クライアントは admin シェルをロードさえできない）。
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

// canonicalPath はルール照合の前にリクエストパスを正規化する。これにより
// /api/plugins/x/../upload のような別名が、それが解決する実パスより弱いルールに
// マッチできないようにする（G-4）。net/http の mux は別名をこの正規形へ
// リダイレクトするため、チェックと最終的に配信されるルートが常に一致する。
// canonicalPath normalizes a request path before rule matching, so that an
// alias such as /api/plugins/x/../upload cannot match a weaker rule than the
// real path it resolves to (G-4). net/http's mux redirects the alias to this
// canonical form, so the checks and the finally-served route always agree.
func canonicalPath(p string) string {
	return path.Clean(p)
}

// minRoleFor は HTTP メソッドと API パスの最小ロールと、それがルールで
// カバーされているかを返す。空でない method を持つルールはそのメソッドにのみ
// 適用される。ルールの無いパスは viewer（任意のログイン）へフォールスルーする。
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

// Wrap は next を認証/認可チェックで包んで返す。
// Wrap returns next wrapped with the authentication/authorization checks.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// G-4: パスを正規化してからルール照合する。/api/plugins/../config の
		// ような別名が本来より緩いルール（前方一致）にマッチして権限判定を
		// すり抜けるのを防ぐ。mux 側も正規形へリダイレクトするため、ここで
		// 判定するパスと最終的に処理されるパスが一致する。
		// G-4: normalize the path before rule matching. Prevent an alias like
		// /api/plugins/../config from matching a weaker (prefix) rule and
		// slipping past the authorization check. The mux also redirects to
		// the canonical form, so the path judged here and the path finally
		// served always agree.
		cleanPath := canonicalPath(r.URL.Path)

		// CSRF 多層防御。auth がオフでも適用する（無害で、チェックを 1 箇所に
		// 保てる）。SameSite=Lax は既にクロスサイト POST でセッション cookie を
		// ブロックするが、Origin の検証がその隙間を塞ぐ。
		// CSRF defense in depth, applied even when auth is off (harmless and
		// keeps the check in one place). SameSite=Lax already blocks the
		// session cookie on cross-site POSTs; verifying Origin closes the gap.
		if !csrfOriginOK(r) {
			writeError(w, http.StatusForbidden, "クロスサイトリクエストを拒否しました")
			return
		}

		if !m.enabled {
			// auth が無効（信頼ネットワークモード）なので、ルートは通常オープンだ。
			// プラグインアップロードは例外だ。未認証の .so アップロードはリモート
			// コード実行なので、auth がオンになるまで拒否する。
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

		// Agent の WebSocket 接続はセッション cookie ではなく共有トークンで認証する。
		// それらを /ws ハンドラへ通し、そこでトークンを検証する。これが無いと
		// auth を有効にした途端に agent が完全にブロックされる。
		// Agent WebSocket connections authenticate with a shared token, not
		// a session cookie. Let them through to the /ws handler, which
		// validates the token. Without this, enabling auth would block the
		// agent entirely.
		if path == "/ws" && r.URL.Query().Get("role") == "agent" && m.handler.agentToken != "" {
			next.ServeHTTP(w, r)
			return
		}

		// セットアップフロー: ユーザーが存在しない場合、ブラウザを /setup.html へ
		// 強制する（または setup API を許可する）。最初の admin を作成できるようにだ。
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

		// 公開パス（ログインページ、health、auth status）は常に許可する。
		// Public paths (login page, health, auth status) are always allowed.
		if isPublicPath(path) {
			next.ServeHTTP(w, r)
			return
		}

		// ログイン不要 viewer モード: dashboard ページ、そのアセット、/ws、
		// /api/status はセッション無しで配信される。セッションが「存在する」場合は
		// フォールスルーし、通常のロールチェック（および /ws では agent 経路）が
		// 依然適用される。
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

		// それ以外はすべて有効なセッションを要する。
		// Everything else requires a valid session.
		sess, ok := m.handler.sessionFromRequest(r)
		if !ok {
			if strings.HasPrefix(path, "/api/") || path == "/ws" {
				writeError(w, http.StatusUnauthorized, "ログインが必要です")
				return
			}
			// 静的ページ: ブラウザをログインページへ送る。
			// Static pages: send the browser to the login page.
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}

		// /api パスとプラグイン Web UI では最小ロールを強制する。
		// For /api paths and plugin web UIs, enforce the minimum role.
		if strings.HasPrefix(path, "/api/") || path == "/ws" || strings.HasPrefix(path, "/plugins/") {
			min, _ := minRoleFor(r.Method, path)
			if !sess.Role.AtLeast(min) {
				writeError(w, http.StatusForbidden, "権限が不足しています")
				return
			}
		}

		// ロール制限された静的ページをサーバー側でもゲートし、admin UI シェルが
		// 低いロールへ配信されることが決してないようにする（フロントエンドも
		// リダイレクトするが、それを唯一の障壁にしてはならない）。
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
