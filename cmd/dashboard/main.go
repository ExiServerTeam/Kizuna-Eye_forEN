package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"Kizuna-Eye/internal/api"
	"Kizuna-Eye/internal/auth"
	"Kizuna-Eye/pkg/alert"
	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/insight"
	"Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/logsafe"
	"Kizuna-Eye/pkg/notify"
	"Kizuna-Eye/pkg/report"
	"Kizuna-Eye/pkg/status"
)

var upgrader = websocket.Upgrader{
	// 同一オリジンのブラウザ接続と、Origin ヘッダを送らない非ブラウザクライアント
	// （Agent）を許可する。クロスオリジンのリクエストを拒否すると、悪意あるページが
	// 被害者のセッション cookie で /ws を開くクロスサイト WebSocket ハイジャック
	// （CSWSH）を防げる。
	// Allow same-origin browser connections, and non-browser clients
	// (the Agent) which send no Origin header. Rejecting cross-origin
	// requests prevents cross-site WebSocket hijacking (CSWSH), where a
	// malicious page opens /ws with the victim's session cookie.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // 非ブラウザクライアント（例: Agent） / non-browser client (e.g. the Agent)
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

// nextWeekly は、now のロケーションで、now より厳密に後の指定曜日（0=日..6=土）と
// 時刻の次の発生を返す。
// nextWeekly returns the next occurrence of the given weekday (0=Sun..6=Sat)
// and hour strictly after now, in now's location.
func nextWeekly(day, hour int, now time.Time) time.Time {
	if day < 0 || day > 6 {
		day = 1
	}
	if hour < 0 || hour > 23 {
		hour = 9
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	for i := 0; i < 8; i++ {
		if int(t.Weekday()) == day && t.After(now) {
			return t
		}
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// postureFromConfig は dashboard 設定ファイルからポスチャスコアの入力を導出する。
// config.LoadDashboardConfig を呼ぶのではなくファイルを直接読んでデコードする。
// あのローダーは stdout へ出力し、ファイル権限を締め、既定値を適用するため、
// 毎回の /api/insight リクエスト（dashboard は開いているタブごとに 60 秒ごとに
// ポーリングする）で呼ぶとログを溢れさせ、設定ファイルを繰り返し chmod していた。
// GET は副作用を持ってはならない。ファイルが読めない場合は代わりに起動時設定を
// 使い、一時的な読み取りエラーが誤解を招く「すべてオフ」を報告しないようにする。
// postureFromConfig derives the posture score input from the dashboard config
// file. It reads and decodes the file directly instead of calling
// config.LoadDashboardConfig: that loader prints to stdout, tightens file
// permissions and applies defaults, so invoking it on every /api/insight
// request (the dashboard polls it every 60s per open tab) spammed the log and
// chmod'd the config file repeatedly. A GET must not have side effects. When
// the file cannot be read the startup config is used instead, so a transient
// read error never reports a misleading "everything is off".
func postureFromConfig(path string, fallback *config.DashboardConfig) insight.PostureInput {
	dc := fallback
	if data, err := os.ReadFile(path); err == nil {
		var loaded config.DashboardConfig
		if json.Unmarshal(data, &loaded) == nil {
			dc = &loaded
		}
	}
	if dc == nil {
		return insight.PostureInput{}
	}
	return insight.PostureInput{
		AuthEnabled: dc.Auth.Enabled,
		// IsPublicViewer ではなく config_version を考慮した解決を使う。この struct は
		// 既定値を事前充填しない素の json.Unmarshal で生成されたため、セキュアな設定
		// （config_version>=2）で public_viewer が欠落していれば、ここでも OFF と
		// 読まなければならない。そうでないとセキュリティスコアが匿名閲覧を有効と
		// 誤って報告する。
		// Use the config_version-aware resolution, not IsPublicViewer: this
		// struct was produced by a bare json.Unmarshal that did not pre-fill
		// defaults, so an absent public_viewer on a secure config
		// (config_version>=2) must read as OFF here too — otherwise the
		// security score would wrongly report anonymous viewing as enabled.
		PublicViewerEnabled:  dc.Auth.EffectivePublicViewer(dc.ConfigVersion),
		SecureCookies:        dc.Auth.SecureCookies,
		AgentTokenSet:        strings.TrimSpace(dc.Auth.AgentToken) != "",
		RequireSignature:     dc.Plugins.RequireSignature,
		PublicKeySet:         strings.TrimSpace(dc.Plugins.PublicKeyFile) != "",
		AlertHistorySigned:   strings.TrimSpace(dc.AlertHistoryKeyPath) != "",
		NotificationsEnabled: dc.Notifications.Enabled,
		ListenRestricted:     isLoopbackListen(dc.ListenAddr),
	}
}

// isLoopbackListen は listen アドレスがループバックインターフェースのみに
// 束縛されているかを返す。"127.0.0.1:8080"、"localhost:8080"、"[::1]:8080" は
// 制限済みで、":8080" と "0.0.0.0:8080" はそうではない。アドレスはポートを
// 省略し得る（素のホスト）。その場合、値全体がホストだ。
//
// これはポスチャスコアが使う。リバースプロキシ背後で loopback に束縛するのは
// 推奨配備であり、肯定的な制御として数えられる。
// isLoopbackListen reports whether a listen address is bound to the loopback
// interface only. "127.0.0.1:8080", "localhost:8080" and "[::1]:8080" are
// restricted; ":8080" and "0.0.0.0:8080" are not. The address may omit the
// port (a bare host), in which case the whole value is the host.
//
// This is used by the posture score: binding to loopback behind a reverse
// proxy is the recommended deployment, so it counts as a positive control.
func isLoopbackListen(addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	switch host {
	case "localhost", "127.0.0.1", "::1", "0:0:0:0:0:0:0:1":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// runWeeklyReport は設定された曜日/時刻に毎週セキュリティブリーフィングを送る。
// コンテキストがキャンセルされるまで続く。レポートは現在のアラート履歴
// （直近 7 日）から生成し、指定されたチャンネルにのみ送る。
// runWeeklyReport sends the security briefing every week at the configured
// day/hour until the context is cancelled. The report is generated from the
// current alert history (last 7 days) and sent only to the named channels.
func runWeeklyReport(
	ctx context.Context,
	engine *alert.Engine,
	notifier *notify.Manager,
	lg *logger.Logger,
	day, hour int,
	channels []string,
) {
	for {
		now := time.Now()
		next := nextWeekly(day, hour, now)
		wait := time.Until(next)
		if wait < 0 {
			wait = time.Hour
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		entries := engine.Alerts()
		res := report.Generate(entries, report.Options{
			Period: 7 * 24 * time.Hour,
			Now:    time.Now(),
		})
		notifier.SendToChannels(&notify.Alert{
			Type:      "weekly_report",
			Level:     notify.LevelInfo,
			Icon:      "📊",
			Title:     res.Subject,
			Message:   res.Markdown,
			Timestamp: time.Now(),
		}, channels...)
		if lg != nil {
			lg.InfoT("dash.weekly_report_sent", res.Total, res.Critical)
		}
	}
}

// acceptAgentPayload は、受信した /ws ペイロードを信頼できる agent データとして
// 扱ってよいか（最後の status として保存しブラウザへ再ブロードキャストする）、
// またレガシーのトークン無しヒューリスティックで接続を agent へ昇格すべきかを返す。
//
// isAgent は接続が agent として認証済み（有効な共有トークン、または既に昇格済み）
// なら true だ。canPromote は、トークンが設定されていないときに role=agent を
// 宣言した非ブラウザ接続にのみ true だ。素のブラウザ（isAgent=false、
// canPromote=false）は決して受け入れてはならず、メトリクス/履歴を偽装したり
// agent を乗っ取ったりできない。
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

// eligibleForHeuristicPromotion は、/ws 接続がレガシーのトークン無しヒューリスティックで
// agent になれるかを返す。トークンが設定されていないこと、クライアントが
// role=agent を宣言したこと、クライアントがブラウザで「ない」ことを要求する。
// ブラウザは WebSocket ハンドシェイクで常に Origin ヘッダを送るが、Go agent は
// 送らない。Origin チェックが無いと、ログイン済みの任意のブラウザが
// /ws?role=agent を開いて agent を偽装できてしまう。
//
// さらに、認証が有効（authEnabled）なときは常に false を返す（fail-closed）。
// 認証が有効なら、トークン無しの非ブラウザクライアントに agent を名乗らせては
// ならない。そうしないと、未認証のリモートクライアントが /ws?role=agent を
// 開き、偽の security_alert を注入して通知・アラート履歴を汚染できてしまう。
// トークンが未設定なら agent は接続できない（起動時に dash.agent_token_missing
// で警告済み）。トークン無しヒューリスティックは、認証を切った信頼済み
// ネットワークでのみ意味を持つ。
// eligibleForHeuristicPromotion reports whether a /ws connection may become
// the agent through the legacy tokenless heuristic. It requires that no token
// is configured, that the client declared role=agent, and that the client is
// NOT a browser. Browsers always send an Origin header on the WebSocket
// handshake; the Go agent does not. Without the Origin check, any logged-in
// browser could open /ws?role=agent and impersonate the agent.
//
// It also always returns false when authentication is enabled (authEnabled),
// i.e. fail-closed: a tokenless non-browser client must not be able to claim
// to be the agent, otherwise an unauthenticated remote client could open
// /ws?role=agent and inject forged security_alert events into notifications
// and the alert history. With no token configured the agent simply cannot
// connect (warned at startup via dash.agent_token_missing). The tokenless
// heuristic is meaningful only on a trusted network with auth disabled.
func eligibleForHeuristicPromotion(agentToken string, authEnabled, isAgentRole bool, origin string) bool {
	if authEnabled {
		return false
	}
	return agentToken == "" && isAgentRole && origin == ""
}

// ログへ書く信頼できないプラグインテキストは logsafe.Field（internal/auth と共有）で
// 無害化する。ローカルコピーはタブ・改行を空白に置換し角括弧を放置していたため、
// 細工されたイベントが dashboard.log に "[WARN]" マーカーを偽造できた（監査 L-11）。
// Untrusted plugin text written to the log is neutralised with logsafe.Field
// (shared with internal/auth). The local copy replaced tabs/newlines with a
// space and left brackets alone, so a crafted event could still forge a
// "[WARN]" marker in dashboard.log (audit L-11).

// writeDashboardJSON は Content-Type ヘッダ付きで JSON を書く。
// Cache-Control: no-store は機微な API 応答（ユーザー名/IP を含むログ行、
// マスク済み設定、モジュールパス、セッション単位データ）を共有プロキシや
// ブラウザキャッシュから締め出す。auth ハンドラは既に no-store を設定しており、
// これで dashboard API も一貫する。
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

// runtimeStore は agent のモジュールロード結果（"module_status" イベント由来）を
// 保持し、JSON として提供する。プラグインは agent 上でロードに失敗し得て
// （設定欠落、署名エラーなど）、それを観測できるのは agent だけだ。そうでないと
// dashboard は動作中プラグインと黙って落ちたプラグインを区別できない。
// runtimeStore keeps the agent's module load results (from the
// "module_status" event) and serves them as JSON. A plugin can fail to load
// on the agent (missing config, signature error, ...), and only the agent can
// observe that; the dashboard otherwise has no way to tell a running plugin
// from one that was silently dropped.
type runtimeStore struct {
	mu     sync.RWMutex
	states map[string]runtimeState
}

type runtimeState struct {
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
}

func newRuntimeStore() *runtimeStore {
	return &runtimeStore{states: make(map[string]runtimeState)}
}

// observe は agent メッセージを読み、module_status イベントなら保存済みビューを
// 置き換える。他のメッセージは無視する。
// observe reads an agent message and, when it is a module_status event,
// replaces the stored view. Any other message is ignored.
func (s *runtimeStore) observe(msg []byte) {
	var env struct {
		Event    string                  `json:"event"`
		Statuses map[string]runtimeState `json:"statuses"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		return
	}
	if env.Event != "module_status" {
		return
	}
	s.mu.Lock()
	s.states = env.Statuses
	if s.states == nil {
		s.states = make(map[string]runtimeState)
	}
	s.mu.Unlock()
}

func (s *runtimeStore) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	out := make(map[string]runtimeState, len(s.states))
	for k, v := range s.states {
		out[k] = v
	}
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"modules": out})
}

// handleSecurityEvent は agent の "security_alert" イベントを通知（Discord など）と
// アラート履歴エントリへ変換する。
// handleSecurityEvent turns an agent "security_alert" event into a
// notification (Discord etc.) and an alert-history entry.
func handleSecurityEvent(msg []byte, engine *alert.Engine, lg *logger.Logger) {
	var env struct {
		Event       string `json:"event"`
		Plugin      string `json:"plugin"`
		Category    string `json:"category"`
		Level       string `json:"level"`
		Title       string `json:"title"`
		Message     string `json:"message"`
		Actor       string `json:"actor"`
		IP          string `json:"ip"`
		Command     string `json:"command"`
		DetectFile  string `json:"detect_file"`
		DetectLine  int    `json:"detect_line"`
		Remediation string `json:"remediation"`
		RelatedLog  string `json:"related_log"`
		TitleEN     string `json:"title_en"`
		MessageEN   string `json:"message_en"`
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
	// bodyEn は body を英語ラベルで鏡写しにし、アラート履歴を英語で表示できる
	// ようにする（タスク9）。MessageEN が存在するときのみ意味を持つ
	// （古いプラグインは Message のみ送る）。
	// bodyEn mirrors body with English labels so the alert history can be
	// shown in English (task 9). It is only meaningful when MessageEN exists
	// (older plugins send only Message).
	bodyEn := env.MessageEN
	if env.Actor != "" || env.IP != "" {
		parts := make([]string, 0, 2)
		partsEn := make([]string, 0, 2)
		if env.Actor != "" {
			parts = append(parts, "ユーザー: "+env.Actor)
			partsEn = append(partsEn, "User: "+env.Actor)
		}
		if env.IP != "" {
			parts = append(parts, "接続元: "+env.IP)
			partsEn = append(partsEn, "From: "+env.IP)
		}
		if len(parts) > 0 {
			body += " (" + strings.Join(parts, " / ") + ")"
		}
		if bodyEn != "" && len(partsEn) > 0 {
			bodyEn += " (" + strings.Join(partsEn, " / ") + ")"
		}
	}

	engine.ReportAlertDetail(&notify.Alert{
		Type:      "security_" + env.Category,
		Level:     level,
		Icon:      icon,
		Title:     env.Title,
		Message:   body,
		Timestamp: time.Now(),
	}, "agent", false, alert.AlertDetail{ // agent 報告: 偽造可能なログ行に由来する / agent-reported: derived from a forgeable log line
		Command:     env.Command,
		DetectFile:  env.DetectFile,
		DetectLine:  env.DetectLine,
		Remediation: env.Remediation,
		RelatedLog:  env.RelatedLog,
		TitleEN:     env.TitleEN,
		MessageEN:   bodyEn,
	})

	if lg != nil {
		lg.InfoT("dash.security_alert", logsafe.Field(env.Level), logsafe.Field(env.Title))
	}
}

// ============================================================
// wsClient は WebSocket 接続を専用の書き込みミューテックスで包む。
// gorilla/websocket は接続ごとに 1 つの並行書き手しか許さないが、「異なる」
// 接続への書き込みは並行に進み得る。接続ごとのミューテックスは、1 つの遅い
// クライアントが他の全クライアント（および agent）をブロックするのを防ぐ。
// wsClient wraps a WebSocket connection with its own write mutex.
// gorilla/websocket allows only one concurrent writer per connection,
// but writes to *different* connections may proceed in parallel. A
// per-connection mutex prevents one slow client from blocking every
// other client (and the agent).
// ============================================================
type wsClient struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	// authed はアップグレード時に接続が有効なセッションを運んだとき true だ。
	// public viewer モードでは未認証クライアントは無害化済み status のみを受け取り、
	// セキュリティイベントを受け取らない。
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

// writeControl は writeMessage が使うのと同じ書き込みミューテックス下で制御
// フレーム（ping/pong/close）を送り、ping がブロードキャストと割り込まないようにする。
// writeControl sends a control frame (ping/pong/close) under the same write
// mutex used by writeMessage, so a ping cannot interleave with a broadcast.
func (c *wsClient) writeControl(messageType int, data []byte, deadline time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteControl(messageType, data, deadline)
}

// ============================================================
// Hub は WebSocket クライアントを管理する。
// Hub manages WebSocket clients.
// ============================================================
type Hub struct {
	sync.RWMutex
	clients    map[*wsClient]bool
	maxClients int
	log        *logger.Logger
	lastStatus *status.SystemStatus

	// agentToken は agent 接続を認証する。空はトークン認証を無効化する
	// （レガシーのヒューリスティック検出のみ）。
	// agentToken authenticates agent connections. Empty disables token
	// authentication (legacy heuristic detection only).
	agentToken string

	// authEnabled は dashboard の認証が有効かを示す。認証が有効なときは
	// トークン無しのレガシーヒューリスティック（role=agent + Origin なし）を
	// 無効化する。そうしないと、未認証の非ブラウザクライアントが agent を
	// 名乗り、偽の security_alert を注入できてしまう（fail-closed）。
	// authEnabled indicates whether dashboard authentication is on. When it is,
	// the legacy tokenless heuristic (role=agent with no Origin) is disabled:
	// otherwise an unauthenticated non-browser client could claim to be the
	// agent and inject forged security_alert events (fail-closed).
	authEnabled bool

	// viewerMode は未認証ブラウザクライアントが受け取るものをフィルタする。
	// true のとき、status は無害化され、セッションを提示しなかったクライアントには
	// イベントを送らない。起動中に 1 回設定する。
	// viewerMode filters what unauthenticated browser clients receive: when
	// true, statuses are sanitized and events are withheld for clients that
	// did not present a session. Set once during startup.
	viewerMode bool

	agentConn *wsClient

	// コールバックは起動中に 1 回設定し、WS ゴルーチンから読む。埋め込み
	// RWMutex で保護し、遅い代入が読み手と競合しないようにする
	// （SetCallbacks / onX アクセサを使う）。
	// Callbacks are set once during startup and read from WS goroutines.
	// They are guarded by the embedded RWMutex so a late assignment cannot
	// race with a reader (use SetCallbacks / the onX accessors).
	onAgentDisconnect func()
	onAgentStatus     func(*status.SystemStatus)
	onAgentEvent      func([]byte)

	// コンパクトなサンプル（リング、時系列順）。チャート化されるフィールドのみを
	// 保存しメモリを小さく保ち、1 秒間隔で約 1 時間分が収まるようにする。
	// Compact samples (ring, chronological). Storing only the charted
	// fields keeps memory small so ~1h of 1s-interval data fits.
	samples    []api.HistorySample
	maxSamples int

	// maxViewers は「未認証」WebSocket クライアントの数を制限する。/ws はセッション
	// 無しで到達可能なため、別個の上限が無いと攻撃者が maxClients 個の匿名接続を
	// 開き、固定のクライアントスロットから Agent（および全実ブラウザ）を枯渇させ得る。
	// 認証済みクライアント（Agent とログイン済みユーザー）はこの上限に数えられず、
	// maxClients が許す限り受け入れられるため、監視フィードが匿名接続枯渇で
	// 拒否されることはない。
	// maxViewers caps the number of *unauthenticated* WebSocket clients.
	// The /ws endpoint is reachable without a session, so without a separate
	// cap an attacker could open maxClients anonymous connections and starve
	// the Agent (and every real browser) of the fixed client slots. Authenticated
	// clients (the Agent and logged-in users) are never counted against this
	// limit and are admitted as long as maxClients allows, so the monitoring
	// feed cannot be denied by anonymous connection exhaustion.
	maxViewers int

	// フラッド検知: 拒否された接続のバーストは接続枯渇型 DoS の兆候だ。これが無いと
	// dashboard は拒否をログするだけでアラートを出さず、運用者はサービスが攻撃下に
	// あることを知れなかった。onFlood は拒否が窓内で floodThreshold を超えたとき
	// （floodWindow ごとに最大 1 回）呼ばれる。
	// Flood detection: a burst of rejected connections is the signature of a
	// connection-exhaustion DoS. Without this, the dashboard only logged the
	// rejections and never raised an alert, so an operator could not tell that
	// the service was under attack. onFlood is called (at most once per
	// floodWindow) when rejections cross floodThreshold inside a window.
	onFlood func(rejected int, detail string)
	// floodMu は下のカウンタを保護する。
	// floodMu guards the counters below.
	floodMu       sync.Mutex
	floodCount    int
	floodWindowAt time.Time

	// onAuthReject は疑わしい agent 接続が拒否されたとき（authRejectWindow ごと、
	// 理由ごとに最大 1 回）呼ばれる。これが無いと、トークンブルートフォース /
	// agent 乗っ取りの試みはログの WARN 行でしかなく、運用者に届かなかった。
	// onAuthReject is called (at most once per authRejectWindow and
	// per reason) when suspicious agent connections are rejected.
	// Without it, token brute-force / agent hijack attempts were only
	// WARN lines in the log and never reached the operator.
	onAuthReject func(reason string, count int)
	// authRejectMu は下の authRejects を保護する。
	// authRejectMu guards authRejects below.
	authRejectMu sync.Mutex
	authRejects  map[string]*authRejectCounter
}

// floodThreshold は floodWindow 内の何件の拒否接続がフラッドアラートを上げるかで、
// floodWindow は測定窓だ。10 秒で 200 拒否は通常運用よりはるかに多く、実際の
// フラッドが生む量（DoS テストは数万を送った）よりはるかに少ない。
// floodThreshold is how many rejected connections within floodWindow raise a
// flood alert, and floodWindow is the measurement window. 200 rejections in 10
// seconds is far above normal operation but well below what a real flood
// produces (the DoS tests sent tens of thousands).
const (
	floodThreshold = 200
	floodWindow    = 10 * time.Second
)

// noteRejected は拒否された接続を記録し、閾値を超えたとき onFlood を発火する。
// 窓は各アラート後にリセットされ、アラートは拒否ごとではなく floodWindow ごとに
// 最大 1 回上がる。
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
	fire := count == floodThreshold // 窓ごとにちょうど 1 回 / exactly once per window
	cb := h.onFlood
	h.floodMu.Unlock()
	if fire && cb != nil {
		cb(count, detail)
	}
}

// authRejectThreshold は authRejectWindow 内で「同じ」理由の疑わしい agent 接続が
// 何件で 1 つのアラートを上げるかで、authRejectWindow は測定窓だ。60 秒で 10 回は
// 通常（健全な agent は 1 回接続する）よりはるかに多く、実際のブルートフォースの
// バーストよりは少ない。
// authRejectThreshold is how many suspicious agent connections with the
// SAME reason within authRejectWindow raise one alert, and authRejectWindow
// is the measurement window. 10 attempts in 60s is well above normal
// (a healthy agent connects once) but below a real brute-force burst.
const (
	authRejectThreshold = 10
	authRejectWindow    = 60 * time.Second
)

// authRejectCounter は 1 つの拒否理由の窓付きカウントを追跡する。
// authRejectCounter tracks one rejection reason's windowed count.
type authRejectCounter struct {
	count    int
	windowAt time.Time
}

// noteAuthReject は疑わしい agent 接続を記録し、閾値に達したとき onAuthReject を
// 発火する。窓は各アラート後にリセットされ、アラートは窓ごと・理由ごとに最大 1 回
// 上がる。
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

// SetAuthRejectCallback は疑わしい agent 接続が検出されたときに呼ばれる
// コールバックを設定する。起動中に 1 回呼ぶ。
// SetAuthRejectCallback installs the callback invoked when suspicious
// agent connections are detected. Call once during startup.
func (h *Hub) SetAuthRejectCallback(fn func(reason string, count int)) {
	h.authRejectMu.Lock()
	h.onAuthReject = fn
	h.authRejectMu.Unlock()
}

// SetFloodCallback は接続フラッドが検出されたときに呼ばれるコールバックを設定する。
// 起動中に 1 回呼ぶ。
// SetFloodCallback installs the callback invoked when a connection flood is
// detected. Call once during startup.
func (h *Hub) SetFloodCallback(fn func(rejected int, detail string)) {
	h.floodMu.Lock()
	h.onFlood = fn
	h.floodMu.Unlock()
}

func NewHub(maxClients int, log *logger.Logger) *Hub {
	// クライアントスロットの大多数を認証済みクライアント（Agent + ログイン済み
	// ブラウザ）用に確保し、匿名 viewer がそれらを枯渇させないようにする。
	// public-viewer モード用に妥当な数の viewer スロットを保つ。
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
		maxSamples: 3600, // Agent の 1 秒間隔で約 1 時間 / ~1h at the Agent's 1s interval
	}
}

// SetCallbacks は agent コールバックを hub ロック下で設定する。3 つのいずれも
// nil にしてクリアできる。
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

// SetViewerMode は public-viewer フィルタリングを有効化する。status ブロードキャストは
// 無害化され、認証されていないクライアントにはイベントを送らない。これにより未認証
// viewer はライブ WebSocket フィード経由でプロセス一覧やディスク S.M.A.R.T メタデータを
// 読めない。起動中に 1 回呼ぶ。
// SetViewerMode enables public-viewer filtering: status broadcasts are
// sanitized and events are withheld for clients that are not authenticated,
// so an unauthenticated viewer cannot read the process list or disk S.M.A.R.T
// metadata through the live WebSocket feed. Call once during startup.
func (h *Hub) SetViewerMode(enabled bool) {
	h.Lock()
	h.viewerMode = enabled
	h.Unlock()
}

// sanitizeStatusJSON は msg が status ペイロードでなければそのまま返し、そうであれば
// viewer 制限フィールドを除去する。非 status メッセージ（agent コマンド/イベント）は
// そのまま返す。
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

// SetOnAgentEvent は agent-event コールバックのみを設定する。
// SetOnAgentEvent installs just the agent-event callback.
func (h *Hub) SetOnAgentEvent(fn func([]byte)) {
	h.Lock()
	h.onAgentEvent = fn
	h.Unlock()
}

// onDisconnectFn は読み取りロック下で disconnect コールバックを返す。
// onDisconnectFn returns the disconnect callback under the read lock.
func (h *Hub) onDisconnectFn() func() {
	h.RLock()
	defer h.RUnlock()
	return h.onAgentDisconnect
}

// onEventFn は読み取りロック下で event コールバックを返す。
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

	// viewer 向け無害化形をクライアントごとではなく 1 回計算し、N 人の公開 viewer が
	// ブロードキャストごとに N 回の JSON unmarshal/marshal サイクルを起こさないようにする。
	// 未認証 viewer が少なくとも 1 人接続しているときのみ必要で、認証済みクライアント
	// のみ（通常ケース）ならブロードキャストごとの unmarshal+marshal は完全に省かれる。
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
			h.log.DebugT("dash.broadcast_error", err)
			h.Remove(c)
		}
	}
}

// Add は WebSocket クライアントを登録する。isAgent は唯一の信頼できる Agent 接続を
// 示し、authed はログイン済みブラウザを示す。認証済みクライアント（Agent と
// ログイン済みユーザー）は maxClients まで受け入れる。未認証クライアントはさらに
// maxViewers で制限し、匿名接続が固定のクライアントスロットを枯渇させ、Agent や
// 実ユーザーへのサービスを拒否できないようにする。
// Add registers a WebSocket client. isAgent marks the single trusted Agent
// connection; authed marks a logged-in browser. Authenticated clients (Agent
// and logged-in users) are admitted up to maxClients. Unauthenticated clients
// are additionally capped at maxViewers so anonymous connections cannot
// exhaust the fixed client slots and deny service to the Agent or real users.
func (h *Hub) Add(conn *websocket.Conn, authed, isAgent bool) (*wsClient, bool) {
	h.Lock()
	defer h.Unlock()

	// viewer 固有の上限のために未認証 viewer を数える。Agent と認証済みブラウザは
	// クライアントスロットも占めるが viewer ではない。
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
			h.log.WarnT("dash.viewer_cap", h.maxViewers)
			h.noteRejected("unauthenticated viewer cap")
			return nil, false
		}
	}

	if h.maxClients > 0 && len(h.clients) >= h.maxClients {
		// 認証済みクライアントが優先される。スロットが匿名 viewer に占められていれば、
		// Agent / 実ユーザーが接続できるようそれを追い出す。
		// Authenticated clients take priority: if the slot is occupied by an
		// anonymous viewer, evict it so the Agent / a real user can connect.
		if authed || isAgent {
			if evicted := h.evictViewerLocked(); evicted {
				h.log.WarnT("dash.evict_viewer", h.maxClients)
			} else {
				h.log.WarnT("dash.max_clients", h.maxClients)
				h.noteRejected("max clients reached")
				return nil, false
			}
		} else {
			h.log.WarnT("dash.max_clients", h.maxClients)
			h.noteRejected("max clients reached")
			return nil, false
		}
	}

	client := &wsClient{conn: conn, authed: authed || isAgent}
	h.clients[client] = true
	h.log.DebugT("dash.client_added", conn.RemoteAddr(), len(h.clients))
	return client, true
}

// evictViewerLocked は認証済みクライアント用にスロットを空けるため、未認証 viewer を
// 1 つ切断する。呼び出し側は h.Lock を保持すること。viewer を追い出したとき true を
// 返す。接続の close はゴルーチンへ延期し、hub ロックを保持したまま実行しない。
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
		h.log.DebugT("dash.client_removed", client.conn.RemoteAddr(), len(h.clients))
	}
	wasAgent := (h.agentConn == client)
	if wasAgent {
		h.agentConn = nil
	}
	h.Unlock()

	// コールバックはロック解放後に呼ぶ。コールバックはアラートエンジンへ到達し
	// （将来 hub に再び触れるかもしれない）、ロック下で呼ぶとデッドロックの危険が
	// ある。ロック付きアクセサ経由で読み、並行する SetCallbacks がここで競合しない
	// ようにする。
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
		h.log.DebugT("dash.client_close_error", err)
	}
}

func (h *Hub) GetLastStatus() *status.SystemStatus {
	h.RLock()
	defer h.RUnlock()
	if h.lastStatus == nil {
		return nil
	}
	// ディープコピーし、呼び出し側が保存済み status（とその共有スライス/ポインタ）を
	// 誤って変更できないようにする。
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
	// 範囲外の値を拒否し、バグ持ち・敵対的 agent が保存済み status、メトリクス履歴、
	// Prometheus エンドポイントを汚染できないようにする（例: cpu_usage=1e9 や負の
	// パーセント）。
	// Reject out-of-range values so a buggy or hostile agent cannot pollute
	// the stored status, the metrics history, or the Prometheus endpoint
	// (e.g. cpu_usage=1e9 or negative percentages).
	if err := s.Validate(); err != nil {
		if h.log != nil {
			h.log.WarnT("dash.invalid_status", err)
		}
		h.Unlock()
		return
	}
	// ディープコピーを保存し、保存済み status が呼び出し側のオブジェクトと
	// スライス/ポインタを共有しないようにする。
	// Store a deep copy so the stored status does not share slices/pointers
	// with the caller's object.
	h.lastStatus = s.Clone()
	// history API 用にコンパクトなサンプルを記録する。
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

// HistorySamples は最近のコンパクトなサンプルを返す（古い順）。
// HistorySamples returns recent compact samples (oldest first).
func (h *Hub) HistorySamples() []api.HistorySample {
	h.RLock()
	defer h.RUnlock()
	out := make([]api.HistorySample, len(h.samples))
	copy(out, h.samples)
	return out
}

// MarkAgentConn は client を agent 接続として登録する。別の agent 接続が既に
// 登録されている場合は false を返し、同じトークンを提示する 2 番目のクライアントが
// 実 agent を追い出せないようにする。これが無いと、agent トークンを知った者
// （例: admin セッション経由）が繰り返し接続して実 agent を追い出し、監視フィードを
// フラップさせ得る。
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

// AgentConnected は Agent が現在接続されているかを返す。
// AgentConnected reports whether an Agent is currently connected.
func (h *Hub) AgentConnected() bool {
	h.RLock()
	defer h.RUnlock()
	return h.agentConn != nil
}

// ★ Phase 8-5: Agent へメッセージを送信する
// ★ Phase 8-5: send a message to the Agent
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
// ★ Phase 8-5: relay to all clients other than the Agent (browsers)
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
		// セキュリティイベントは actor/IP データを運ぶため、未認証 viewer へ
		// 中継しない。
		// Security events carry actor/IP data; do not relay them to
		// unauthenticated viewers.
		if viewerMode && !c.authed {
			continue
		}
		if err := c.writeMessage(websocket.TextMessage, msg, 30*time.Second); err != nil {
			h.log.DebugT("dash.broadcast_browsers_error", err)
			h.Remove(c)
		}
	}
}

// ============================================================
// main（エントリポイント）
// main (entry point)
// ============================================================
func main() {
	configPath := flag.String("config", "dashboard_config.json", "設定ファイルパス")
	flag.Parse()

	cfg, err := config.LoadDashboardConfig(*configPath)
	if err != nil {
		log.Fatalf("設定読み込み失敗: %v", err)
	}

	// 既定は INFO とし、dashboard_config.json の log_level（"debug"）で明示的に
	// 有効化しない限り DEBUG ノイズを出さない。
	// Default to INFO so DEBUG noise is not emitted unless explicitly
	// enabled via dashboard_config.json's log_level ("debug").
	level := logger.INFO
	if cfg.LogLevel != "" {
		level = logger.ParseLevel(cfg.LogLevel)
	}

	lg := logger.NewLogger(&logger.Options{
		LogFile:        cfg.LogFile,
		Level:          level,
		Prefix:         "[DASHBOARD]",
		UseUTC:         false,
		ForwardURL:     cfg.LogForward.URL,
		ForwardTimeout: cfg.LogForward.Timeout(),
	})
	defer lg.Sync()

	lg.InfoT("dash.start", cfg.ListenAddr)
	if cfg.StaticDir != "" {
		lg.InfoT("dash.static_dir", cfg.StaticDir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		lg.InfoT("dash.shutdown_signal")
		cancel()
	}()

	notifierMgr := notify.FromConfig(cfg.Notifications, lg)
	if notifierMgr.HasChannels() {
		lg.InfoT("dash.notify_on")
	} else {
		lg.InfoT("dash.notify_off")
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

	// アラート履歴を再起動をまたいで永続化する。
	// Persist alert history across restarts.
	historyFile := cfg.AlertHistoryFile
	if historyFile == "" {
		historyFile = "logs/alert_history.jsonl"
	}
	// アラート履歴行の任意の HMAC 署名（専用鍵。セキュリティログのチェーン鍵は決して
	// 使わない）。鍵をロードできなければ、起動失敗ではなく未署名永続化へフォールバック
	// する。履歴は依然プラグインの V2-B 整合性チェックで保護される。
	// Optional HMAC signing of alert history lines (dedicated key; never the
	// security-log chain key). If the key cannot be loaded, fall back to
	// unsigned persistence rather than failing to start: the history is still
	// protected by the plugin's V2-B consistency check.
	var alertHistKey []byte
	if cfg.AlertHistoryKeyPath != "" {
		k, kerr := alert.LoadOrCreateAlertHistoryKey(cfg.AlertHistoryKeyPath)
		if kerr != nil {
			lg.WarnT("dash.alert_history_key_failed", kerr)
		} else {
			alertHistKey = k
			lg.InfoT("dash.alert_history_signed", cfg.AlertHistoryKeyPath)
		}
	}
	engine.EnableHistoryPersistenceKeyed(historyFile, alertHistKey)
	lg.InfoT("dash.alert_history", historyFile)

	// 兄弟設定パスを dashboard 設定からの相対で解決し、カスタム -config パスでも
	// agent/modules 設定を同じディレクトリに保つ。そうでないと黙って現在の作業
	// ディレクトリから読み（ロードしたのとは別のファイルを編集して）しまう。
	// Resolve sibling config paths relative to the dashboard config, so a
	// custom -config path keeps the agent/modules configs in the same
	// directory instead of silently reading them from the current working
	// directory (which would edit a different file than the one loaded).
	configDir := filepath.Dir(*configPath)
	agentConfigPath := filepath.Join(configDir, "agent_config.json")
	modulesPath := filepath.Join(configDir, "modules.json")
	storage := api.NewModulesStorage(modulesPath)
	if err := storage.Load(); err != nil {
		lg.WarnT("dash.modules_load_error", err)
	}
	lg.InfoT("dash.modules_loaded", modulesPath, len(storage.GetAll()))

	hub := NewHub(100, lg)
	hub.agentToken = cfg.Auth.AgentToken
	hub.authEnabled = cfg.Auth.Enabled
	// public viewer フィルタリングは auth が有効なときのみ意味を持つ。auth がオフなら
	// 全クライアントが信頼され、従来どおり完全なデータを配信する。
	// Public viewer filtering only matters when auth is enabled; with auth off
	// every client is trusted and full data is served as before.
	hub.SetViewerMode(cfg.Auth.Enabled && cfg.Auth.IsPublicViewer())
	hub.SetCallbacks(engine.OnAgentDisconnect, engine.OnStatus, nil)
	// 接続フラッドが検出されたときセキュリティアラートを上げ、運用者が DoS を
	// dashboard と通知チャンネルで見られるようにする。ログの WARN 行だけにしない。
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
			TitleEN:   "Connection flood detected",
			MessageEN: fmt.Sprintf("Rejected %d connections in a short time (%s). Possible connection-exhaustion DoS attack.", rejected, detail),
		}, "dashboard", true)
		lg.WarnT("dash.flood_detected", rejected, detail)
	})
	// 疑わしい agent 接続（トークン不一致または重複 agent）が拒否されたとき警告を
	// 上げ、ブルートフォース/乗っ取りの試みがログだけでなく運用者に届くようにする。
	// Raise a warning when suspicious agent connections (token mismatch
	// or a duplicate agent) are rejected, so brute-force / hijack attempts
	// reach the operator instead of only the log.
	hub.SetAuthRejectCallback(func(reason string, count int) {
		title := "不正なAgent接続を検知"
		titleEn := "Invalid Agent connection detected"
		switch reason {
		case "agent_token_mismatch":
			title = "Agentトークン不一致（偽装の可能性）"
			titleEn = "Agent token mismatch (possible spoofing)"
		case "agent_duplicate":
			title = "2つ目のAgent接続（乗っ取りの可能性）"
			titleEn = "Second Agent connection (possible hijack)"
		}
		engine.ReportAlert(&notify.Alert{
			Type:      reason,
			Level:     notify.LevelWarning,
			Icon:      "⚠️",
			Title:     title,
			Message:   fmt.Sprintf("短時間に %d 回の不正なAgent接続を拒否しました（%s）。", count, reason),
			Timestamp: time.Now(),
			TitleEN:   titleEn,
			MessageEN: fmt.Sprintf("Rejected %d invalid Agent connections in a short time (%s).", count, reason),
		}, "dashboard", true)
		lg.WarnT("dash.auth_reject_detected", reason, count)
	})
	if cfg.Auth.AgentToken != "" {
		lg.InfoT("dash.agent_token_on")
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

	// authHandler は起動の後半で生成される。下のクロージャはこの変数を参照し、
	// リクエスト時に認証済みブラウザと公開 viewer を区別できるようにする。
	// authHandler is created later in startup; the closures below reference
	// this variable so they can tell an authenticated browser from a public
	// viewer at request time.
	var authHandler *auth.Handler

	mux := http.NewServeMux()

	// ---- WebSocket ハンドラ ----
	// ---- WebSocket handler ----
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// agent トークンが設定されているとき、agent 接続（?role=agent）は一致する
		// トークンを提示しなければならない。ブラウザは role=agent を決して送らない
		// ため、これでログインを迂回することはできない。
		// When an agent token is configured, an agent connection
		// (?role=agent) must present the matching token. Browsers never
		// send role=agent, so this cannot be used to bypass login.
		isAgentRole := r.URL.Query().Get("role") == "agent"
		if hub.agentToken != "" && isAgentRole {
			// G-2: トークンはヘッダでのみ受け取る。クエリ文字列（?token=）は
			// アクセスログ・Referer・ブラウザ履歴に残るため受け付けない。
			// G-2: accept the token only in the header. A query string
			// (?token=) is refused because it lingers in access logs,
			// Referer, and browser history.
			token := r.Header.Get("X-Kizuna-Agent-Token")
			if subtle.ConstantTimeCompare([]byte(token), []byte(hub.agentToken)) != 1 {
				http.Error(w, "invalid agent token", http.StatusUnauthorized)
				lg.WarnT("dash.agent_token_mismatch", r.RemoteAddr)
				hub.noteAuthReject("agent_token_mismatch")
				return
			}
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			lg.ErrorT("dash.ws_upgrade_failed", err)
			return
		}

		authed := authHandler != nil && authHandler.Authenticated(r)
		// Agent は上の共有トークンで認証する（isAgentRole + トークン一致）。
		// 信頼できるクライアントとして扱い、匿名 viewer に数えず、常にスロットを
		// 勝ち取るようにする。
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

		lg.InfoT("dash.client_connected", conn.RemoteAddr())

		// 受信メッセージサイズを制限し、巨大なフレームがメモリを枯渇させない
		// ようにする。status メッセージ（プロセス/ディスク）は数百 KB 未満だ。
		// Cap incoming message size so a huge frame cannot exhaust memory.
		// A status message (processes/disks) is well under a few hundred KB.
		conn.SetReadLimit(8 << 20) // 8 MiB

		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(120 * time.Second))
			return nil
		})

		// 接続を保つ。ブラウザはアプリケーションデータを決して送らないため、
		// サーバー起点の ping が無いと 120 秒の読み取り期限が切れ、約 2 分ごとに
		// 全ブラウザタブが落ちる。
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
			// agent は共有トークンで認証したときのみ即座に信頼される。トークンが
			// 設定されていない場合はレガシーのヒューリスティックへフォールバック
			// するが、非ブラウザクライアントのみだ
			// （eligibleForHeuristicPromotion 参照）。
			// The agent is trusted immediately only when it authenticated
			// with the shared token. When no token is configured, fall back
			// to the legacy heuristic, but only for non-browser clients
			// (see eligibleForHeuristicPromotion).
			isAgent := hub.agentToken != "" && isAgentRole
			canPromoteHeuristically := eligibleForHeuristicPromotion(hub.agentToken, hub.authEnabled, isAgentRole, r.Header.Get("Origin"))
			if isAgent {
				if !hub.MarkAgentConn(client) {
					lg.WarnT("dash.agent_dup_rejected", conn.RemoteAddr())
					hub.noteAuthReject("agent_duplicate")
					conn.WriteMessage(websocket.CloseMessage, []byte("another agent is connected"))
					conn.Close()
					return
				}
				lg.InfoT("dash.agent_authenticated", conn.RemoteAddr())
			}
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
						lg.DebugT("dash.read_error", err)
					}
					break
				}

				conn.SetReadDeadline(time.Now().Add(120 * time.Second))

				// メッセージ種別を判定する。
				// Determine the message type.
				var envelope struct {
					Event  string `json:"event"`
					Action string `json:"action"`
				}
				_ = json.Unmarshal(msg, &envelope)

				if envelope.Event != "" {
					// イベントを発信できるのは Agent だけだ。event 形状のメッセージを
					// 送るブラウザが通知（例: 偽の security_alert）やアラート履歴
					// エントリを偽造できてはならない。
					// Only the Agent may originate events. A browser sending an
					// event-shaped message must not be able to forge notifications
					// (e.g. a fake security_alert) or alert-history entries.
					if !isAgent {
						lg.DebugT("dash.event_ignored", envelope.Event)
						continue
					}
					lg.DebugT("dash.event_received", envelope.Event)
					if fn := hub.onEventFn(); fn != nil {
						fn(msg)
					}
					hub.BroadcastToBrowsers(msg)
					continue
				}

				if envelope.Action != "" {
					lg.DebugT("dash.cmd_ignored", envelope.Action)
					continue
				}

				// status として扱う。正のタイムスタンプを要求し、任意の JSON が
				// status と誤認されないようにする。
				// Treat as a status. A positive timestamp is required so
				// arbitrary JSON is not mistaken for a status.
				var s status.SystemStatus
				if err := json.Unmarshal(msg, &s); err != nil || s.Timestamp <= 0 {
					// ブラウザへ中継するのは agent のペイロードのみだ。非 agent が
					// 任意データを注入・再ブロードキャストできてはならない
					// （例: 他の全クライアントのダッシュボードを空白にする偽の "{}"）。
					// Only the agent's payloads are relayed to browsers. A
					// non-agent must not be able to inject or rebroadcast
					// arbitrary data (e.g. a bogus "{}" that blanks every
					// other client's dashboard).
					if isAgent {
						hub.Broadcast(msg)
					}
					continue
				}
				// status は agent からのみ受け入れる。非 agent 接続は、非ブラウザ
				// クライアント向けのレガシーのトークン無しヒューリスティック経由で
				// のみ agent になれる。素のブラウザは保存済み status を上書き
				// （メトリクス/履歴の偽装）したり agent 接続を乗っ取ったりできては
				// ならない。
				// A status is accepted only from the agent. A non-agent
				// connection may become the agent only through the legacy
				// tokenless heuristic for non-browser clients. A plain
				// browser must not be able to overwrite the stored status
				// (spoofing metrics/history) or hijack the agent connection.
				accept, promote := acceptAgentPayload(isAgent, true, canPromoteHeuristically)
				if !accept {
					lg.DebugT("dash.status_ignored", conn.RemoteAddr())
					continue
				}
				if promote {
					if !hub.MarkAgentConn(client) {
						lg.WarnT("dash.heuristic_rejected", conn.RemoteAddr())
						continue
					}
					isAgent = true
					lg.InfoT("dash.agent_identified", conn.RemoteAddr())
				}
				hub.SetLastStatus(&s)
				hub.Broadcast(msg)
			}
		}()
	})

	// ---- REST API: 最新ステータス ----
	// ---- REST API: latest status ----
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		s := hub.GetLastStatus()
		if s == nil {
			writeDashboardJSON(w, http.StatusNotFound, map[string]string{"error": "no status available"})
			return
		}
		// public viewer モードでは、このエンドポイントがセッション無しで到達可能でも、
		// 未認証クライアントはプロセス一覧やディスク S.M.A.R.T メタデータを見てはならない。
		// In public viewer mode an unauthenticated client must not see the
		// process list or disk S.M.A.R.T metadata, even though this endpoint
		// is otherwise reachable without a session.
		if cfg.Auth.Enabled && cfg.Auth.IsPublicViewer() && (authHandler == nil || !authHandler.Authenticated(r)) {
			s = s.SanitizeForViewer()
		}
		writeDashboardJSON(w, http.StatusOK, s)
	})

	// ---- モジュール管理 API ----
	// ---- Module management API ----
	moduleHandler := api.NewModuleHandler(storage, cfg.ResolvePluginsDir())
	// plugin_path（agent がロードする .so）はコード実行境界なので、その変更には
	// admin が必要だ。パスベースのミドルウェア規則は設定編集とパス差し替えを区別
	// できないため、ハンドラが auth へ直接尋ねる。クロージャはリクエスト時に
	// authHandler を解決する（起動の後半で生成される）。nil ハンドラは「admin でない」
	// （fail-closed）を意味する。
	// plugin_path (the .so the agent loads) is a code-execution boundary, so
	// changing it requires admin. The path-based middleware rule cannot tell a
	// settings edit from a path swap, so the handler asks auth directly. The
	// closure resolves authHandler at request time (it is created later in
	// startup); a nil handler means "not admin" (fail-closed).
	moduleHandler.SetAdminChecker(func(r *http.Request) bool {
		// auth 無効は信頼ネットワークモードを意味する。全リクエストが実質的に
		// admin なので、admin 専用フィールドの変更（およびプラグインアップロード以外の
		// すべて）を許可し、ミドルウェアと一致させる。
		// Auth disabled means trusted-network mode: every request is
		// effectively an admin, so admin-only field changes (and everything
		// else except plugin upload) are allowed, matching the middleware.
		if !cfg.Auth.Enabled {
			return true
		}
		return authHandler != nil && authHandler.IsAdmin(r)
	})
	moduleHandler.RegisterRoutes(mux)

	// collectPluginLogs は modules.json から {moduleName: logPath} を組み立て、
	// ログビューアが logDir の外にあるプラグインログを列挙・配信できるようにする。
	// 使用可能な log_path を持つエントリのみを返す。
	// collectPluginLogs builds {moduleName: logPath} from modules.json so the
	// log viewer can list and serve plugin logs that live outside logDir.
	// Only entries with a usable log_path are returned.
	collectPluginLogs := func(store *api.ModulesStorage) map[string]string {
		out := make(map[string]string)
		if store == nil {
			return out
		}
		for _, m := range store.GetAll() {
			var cfg struct {
				LogPath string `json:"log_path"`
			}
			if len(m.Config) == 0 {
				continue
			}
			if err := json.Unmarshal(m.Config, &cfg); err != nil {
				continue
			}
			if cfg.LogPath == "" {
				continue
			}
			out[m.Name] = cfg.LogPath
		}
		return out
	}

	// ---- プラグイン管理 API（hub を渡す）----
	// ---- Plugin management API (passes hub) ----
	pluginManager, err := api.NewPluginManager(storage, cfg, lg, hub)
	if err != nil {
		lg.ErrorT("dash.pluginmgr_init_failed", err)
		lg.WarnT("dash.plugin_api_disabled")
	} else {
		// プラグインのアップロード/削除（コード実行境界）を、ログだけでなくアラート
		// 履歴と通知に記録する。
		// Record plugin upload/delete (a code-execution boundary) in the
		// alert history and notifications, not only the log.
		pluginManager.SetAuditHook(func(kind, detail, detailEN string) {
			// kind ごとに自然なタイトルを選ぶ。"操作を検知" のような
			// 汎用文言だと何が起きたのか一目で分からないため。
			// Pick a natural title per kind. A generic phrase like "operation
			// detected" does not make clear at a glance what happened.
			title, level, icon := "プラグイン操作を検知", notify.LevelCritical, "🚨"
			titleEn := "Plugin operation detected"
			// サードパーティー製は「未検証（untrusted）」として扱う。
			// ダッシュボードが生成したアラートでも、外部由来のコード追加は
			// 信頼済みバッジを付けるべきではない。
			// Treat third-party as untrusted. Even for a dashboard-generated
			// alert, adding externally-sourced code should not carry a trusted
			// badge.
			isThirdParty := strings.HasPrefix(detail, "サードパーティー製")
			switch kind {
			case "plugin_upload":
				// 純正（Kizuna シリーズ）は警戒、サードパーティー製は重大。
				// 純正は作者が作成したものなので危険度は低いが、追加自体は
				// コード実行境界を越える操作なので警戒で通知する。
				// Official (Kizuna series) is a warning; third-party is
				// critical. Official is lower risk (author-made), but the
				// addition itself crosses a code-execution boundary, so it is
				// reported as a warning.
				title = "プラグインが追加されました"
				titleEn = "Plugin added"
				if isThirdParty {
					level, icon = notify.LevelCritical, "🚨"
				} else {
					level, icon = notify.LevelWarning, "⚠️"
				}
			case "plugin_delete":
				title, level, icon = "プラグインが削除されました", notify.LevelCritical, "🚨"
				titleEn = "Plugin deleted"
			case "plugin_upload_rejected":
				title, level, icon = "プラグインの追加を拒否しました", notify.LevelWarning, "⚠️"
				titleEn = "Plugin addition rejected"
			}
			engine.ReportAlert(&notify.Alert{
				Type:      "audit_" + kind,
				Level:     level,
				Icon:      icon,
				Title:     title,
				Message:   detail,
				Timestamp: time.Now(),
				TitleEN:   titleEn,
				MessageEN: detailEN,
			}, "dashboard", !isThirdParty)
		})
		pluginManager.RegisterRoutes(mux)
		lg.InfoT("dash.api_plugins")
	}

	// moduleRuntime は agent が報告した全モジュールの最後のロード結果を保持する。
	// dashboard はそれを /api/plugin-runtime で公開し、モジュールカードが誤解を招く
	// "active" ではなく "failed to load" を表示できるようにする。
	// moduleRuntime holds the last load result the agent reported for every
	// module. The dashboard exposes it at /api/plugin-runtime so the module
	// card can show "failed to load" instead of a misleading "active".
	moduleRuntime := newRuntimeStore()
	mux.HandleFunc("GET /api/plugin-runtime", moduleRuntime.handle)

	// agent イベントを処理する。プラグイン実行ガードを解放し、モジュールロード結果を
	// 更新し、セキュリティイベントを通知 + アラート履歴へ変換する。
	// Handle agent events: release the plugin run guard, update module load
	// results, and turn security events into notifications + alert history.
	hub.SetOnAgentEvent(func(msg []byte) {
		if pluginManager != nil {
			pluginManager.HandleAgentEvent(msg)
		}
		moduleRuntime.observe(msg)
		handleSecurityEvent(msg, engine, lg)
	})

	// ---- メトリクス API ----
	// ---- Metrics API ----
	metricHandler := api.NewMetricHandler(hub)
	// /api/metrics を /api/status と一致させる。public-viewer モードでは未認証
	// クライアントが無害化済みビュー（プロセス一覧も S.M.A.R.T メタデータも無し）を
	// 受け取る。authHandler は起動の後半で生成されるためリクエスト時に解決する。
	// Keep /api/metrics consistent with /api/status: in public-viewer mode an
	// unauthenticated client gets the sanitized view (no process list, no
	// S.M.A.R.T metadata). authHandler is created later in startup, so it is
	// resolved at request time.
	metricHandler.SetSanitizer(func(r *http.Request) bool {
		return cfg.Auth.Enabled && cfg.Auth.IsPublicViewer() && (authHandler == nil || !authHandler.Authenticated(r))
	})
	metricHandler.RegisterRoutes(mux)
	lg.InfoT("dash.api_metrics")

	// ---- Logs API ----
	// agent ログパスを agent_config.json から解決し、ビューアがハードコードの既定値
	// ではなく実際の設定に追随するようにする。
	// Resolve the agent log path from agent_config.json so the viewer
	// follows the actual configuration instead of a hardcoded default.
	agentLogPath := "logs/agent.log"
	if ac, err := config.LoadAgentConfig(agentConfigPath); err == nil && ac.LogFile != "" {
		agentLogPath = ac.LogFile
	}
	// dashboard は start.sh のシェルリダイレクト（>> logs/dashboard.log）でログを
	// 書くため、cfg.LogFile はしばしば空だ。その既定パスへフォールバックする。
	// そうしないと /api/logs?type=dashboard が空パスへ解決し 400 "Invalid log type"
	// を返す。
	// The dashboard writes its log via the shell redirection in start.sh
	// (>> logs/dashboard.log), so cfg.LogFile is often empty. Fall back to
	// that default path; otherwise /api/logs?type=dashboard resolves to an
	// empty path and returns 400 "Invalid log type".
	dashboardLogPath := cfg.LogFile
	if dashboardLogPath == "" {
		dashboardLogPath = "logs/dashboard.log"
	}
	logHandler := api.NewLogHandler(
		agentLogPath,     // agent ログ / agent log
		dashboardLogPath, // dashboard ログ / dashboard log
		"logs",           // ログディレクトリ / log directory
	)
	// プラグインログは各プラグイン自身のディレクトリ（logDir の外）に書かれるため、
	// 毎リクエストで modules.json を再読み込みするコールバックを登録する。一度きりの
	// スナップショットでは、dashboard が動き続ける間に UI で追加・削除されたモジュールに
	// 追随しない。
	// Plugin logs are written under each plugin's own directory (outside
	// logDir), so register a callback that re-reads modules.json on every
	// request. A one-time snapshot would not follow a module added or
	// removed from the UI while the dashboard keeps running.
	logHandler.SetExtraLogsFunc(func() map[string]string {
		return collectPluginLogs(storage)
	})
	logHandler.RegisterRoutes(mux)
	lg.InfoT("dash.api_logs")

	// ---- 設定 API ----
	// ---- Config API ----
	configHandler := api.NewConfigHandler(
		agentConfigPath,
		*configPath,
		modulesPath,
	)
	configHandler.RegisterRoutes(mux)
	lg.InfoT("dash.api_config")

	// ---- アラート履歴 API ----
	// ---- Alert history API ----
	alertHandler := api.NewAlertHandler(engine)
	alertHandler.RegisterRoutes(mux)
	lg.InfoT("dash.api_alert_history")

	// ---- Security insight API ----
	// アラート履歴を脅威指数 / 攻撃者プロファイルへ集約し、設定ポスチャを採点する。
	// ポスチャコールバックは毎リクエストで dashboard 設定ファイルを再読み込みし、
	// 設定エディタで行われた変更を dashboard 再起動なしで反映する。読み取り失敗は
	// 誤解を招く「すべてオフ」ではなく起動時設定へフォールバックする。
	// Aggregates the alert history into a threat index / attacker profile and
	// scores the configuration posture. The posture callback re-reads the
	// dashboard config file on every request so a change made in the config
	// editor is reflected without restarting the dashboard. A read failure
	// falls back to the startup config rather than reporting a misleading
	// "everything is off".
	insightHandler := api.NewInsightHandler(engine, func() insight.PostureInput {
		return postureFromConfig(*configPath, cfg)
	})
	insightHandler.RegisterRoutes(mux)
	lg.InfoT("dash.api_insight")

	// ---- ワンクリック対処 API（タスク2）----
	// ---- One-click remediation API (task 2) ----
	actionHandler := api.NewActionHandler(engine)
	actionHandler.RegisterRoutes(mux)
	lg.InfoT("dash.api_actions")

	// ---- バージョン API ----
	// ---- Version API ----
	versionHandler := api.NewVersionHandler()
	versionHandler.RegisterRoutes(mux)
	lg.InfoT("dash.api_version")

	// ---- メトリクス履歴 API ----
	// ---- Metrics history API ----
	historyHandler := api.NewHistoryHandler(hub)
	historyHandler.RegisterRoutes(mux)
	lg.InfoT("dash.api_metrics_hist")

	// ---- アラート閾値 API ----
	// ---- Alert threshold API ----
	alertCfgHandler := api.NewAlertConfigHandler(engine, cfg, *configPath, lg)
	alertCfgHandler.RegisterRoutes(mux)
	lg.InfoT("dash.api_alert_settings")

	// ---- Health check ----
	// Agent が接続されていないとき 503 を返し、外部モニタ（Uptime Kuma など）が
	// 劣化状態を検知できるようにする。
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

	// ---- Default language ----
	// i18n.js より前に配信し、ユーザーに保存設定が無いときブラウザが運用者の
	// 設定言語（dashboard_config.json "language"）へフォールバックできるようにする。
	// 未設定/不明なら "en" を既定にする。
	// Served before i18n.js so the browser can fall back to the operator's
	// configured language (dashboard_config.json "language") when the user
	// has no saved preference. Defaults to "en" when unset/unknown.
	uiLang := strings.ToLower(strings.TrimSpace(cfg.Language))
	if uiLang != "ja" && uiLang != "en" {
		uiLang = "en"
	}
	langJS := "window.KIZUNA_DEFAULT_LANG = " + strconv.Quote(uiLang) + ";\n"
	mux.HandleFunc("/lang.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		_, _ = w.Write([]byte(langJS))
	})

	// ---- 静的ファイル ----
	// ---- Static files ----
	staticDir := cfg.StaticDir
	if staticDir == "" {
		staticDir = "./web/static"
	}
	fileServer := http.FileServer(http.Dir(staticDir))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ベースラインのセキュリティヘッダ。UI にインラインスクリプトもインライン
		// イベントハンドラも無いため script-src は 'self' のままでよい。ゲージ幅に
		// インライン style 属性を使うため style-src は 'unsafe-inline' が必要だ。
		// Google Fonts が唯一の外部オリジンだ。
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

		// HTML、JS、CSS は再検証させ、UI/CSS 編集を即座に拾う。.css/.js の長い
		// max-age は、バージョンクエリ（?v=）を変えない限り CSS 修正が
		// ブラウザキャッシュ切れ（24h）まで見えないことを意味し、忘れやすい。
		// 再検証は安価（未変更なら 304）でその落とし穴を除く。画像/フォントは
		// 長いキャッシュを保つ。
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
	// usersFile は既定で dashboard 設定隣の users.json とし、カスタム -config
	// パスでもユーザーストアをその隣に保つ。
	// usersFile defaults to users.json next to the dashboard config, so a
	// custom -config path keeps the user store alongside it.
	usersFile := cfg.Auth.UsersFile
	if usersFile == "" {
		usersFile = filepath.Join(configDir, "users.json")
	}
	userStore := auth.NewStore(usersFile)
	if err := userStore.Load(); err != nil {
		lg.ErrorT("dash.users_load_failed", err)
	}
	sessionTTL := time.Duration(cfg.Auth.SessionTTLHours) * time.Hour
	// 設定された無操作タイムアウト（0 = 既定 2h、負 = 無効）を渡し、運用者が
	// 短い無操作でセッションを失効させるのを止められるようにする。
	// Pass the configured idle timeout (0 = default 2h, negative = disabled)
	// so an operator can stop sessions expiring on short inactivity.
	sessionMgr := auth.NewSessionManager(sessionTTL, cfg.Auth.SessionIdleDuration())
	sessionMgr.SetLogger(lg.Warn)
	if err := sessionMgr.EnablePersistence(cfg.Auth.SessionFilePath(configDir), cfg.Auth.IsSessionIPBind()); err != nil {
		lg.WarnT("dash.session_load_failed", err)
	}
	if p := cfg.Auth.SessionFilePath(configDir); p != "" {
		lg.InfoT("dash.session_persist", p, cfg.Auth.IsSessionIPBind())
	} else {
		lg.InfoT("dash.session_no_persist")
	}
	defer sessionMgr.Stop()
	authHandler = auth.NewHandler(userStore, sessionMgr, lg, cfg.Auth.SecureCookies, cfg.Auth.Enabled, cfg.Auth.AgentToken)
	defer authHandler.Stop()
	// セキュリティ関連の auth イベント（ログインロックアウト = ブルートフォースの
	// 可能性）を、ログだけでなくアラート履歴と通知に記録する。
	// Record security-relevant auth events (login lockouts = likely brute
	// force) in the alert history and notifications, not only the log.
	authHandler.SetSecurityEventHook(func(kind, detail, detailEN, ip string) {
		title := "セキュリティ操作を検知"
		titleEn := "Security operation detected"
		icon := "🔐"
		switch kind {
		case "login_lockout":
			title = "ログイン試行のロックアウト"
			titleEn = "Login attempts locked out"
			icon = "🚨"
		case "user_create":
			title = "ユーザーを作成"
			titleEn = "User created"
			icon = "👤"
		case "user_delete":
			title = "ユーザーを削除"
			titleEn = "User deleted"
			icon = "🗑️"
		case "role_change":
			title = "ロールを変更"
			titleEn = "Role changed"
			icon = "🔑"
		}
		msg := detail
		msgEn := detailEN
		if ip != "" {
			msg += "（接続元: " + ip + "）"
			msgEn += " (from: " + ip + ")"
		}
		engine.ReportAlert(&notify.Alert{
			Type:      "auth_" + kind,
			Level:     notify.LevelCritical,
			Icon:      icon,
			Title:     title,
			Message:   msg,
			Timestamp: time.Now(),
			TitleEN:   titleEn,
			MessageEN: msgEn,
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
		lg.InfoT("dash.public_viewer")
	}

	if cfg.Auth.Enabled {
		switch {
		case userStore.IsCorrupt():
			lg.ErrorT("dash.users_corrupt", usersFile)
		case userStore.NeedsSetup():
			lg.WarnT("dash.no_users")
		default:
			lg.InfoT("dash.auth_on", userStore.Count(), usersFile)
		}
	} else {
		lg.InfoT("dash.auth_off")
	}

	// auth がオンなのに agent トークンが未設定のとき警告する。agent は認証できず、
	// 決して接続しない。
	// Warn when auth is on but no agent token is set: the agent cannot
	// authenticate, so it will never connect.
	if cfg.Auth.Enabled && cfg.Auth.AgentToken == "" {
		lg.WarnT("dash.agent_token_missing")
	}

	// 危険な組み合わせ（プラグインアップロード有効 + 認証オフ）を大声で警告する。
	// ネットワーク上の誰でも .so をアップロード・実行でき、実質リモートコード実行だ。
	// Warn loudly about a dangerous combination: plugin upload enabled while
	// authentication is off. Anyone on the network could upload and run a
	// .so, which is effectively remote code execution.
	if !cfg.Auth.Enabled && cfg.IsUploadEnabled() {
		lg.WarnT("dash.insecure_upload")
	}

	// ---- Weekly security briefing (opt-in) ----
	// 依存の無いスケジューラ（標準 time パッケージ）を使う。dashboard は他に cron
	// ライブラリをインポートしておらず、週 1 回のタイマーのために追加する価値は無い。
	// レポートは設定されたチャンネル（既定 email）にのみ送り、チャットチャンネルを
	// 決してスパムしない。
	// A dependency-free scheduler (standard time package): the dashboard does
	// not otherwise import a cron library, and one weekly timer does not
	// justify adding one. The report is sent only to the configured channels
	// (default email) so it never spams the chat channels.
	if cfg.Notifications.IsWeeklyReportEnabled() {
		day, hour := cfg.Notifications.WeeklyReportSchedule()
		channels := cfg.Notifications.WeeklyReportChannels()
		if notifierMgr.HasChannels() {
			go runWeeklyReport(ctx, engine, notifierMgr, lg, day, hour, channels)
			lg.InfoT("dash.weekly_report_on", day, hour, strings.Join(channels, ","))
		} else {
			lg.WarnT("dash.weekly_report_no_channels")
		}
	}

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: bodyLimitMiddleware(authMiddleware.Wrap(mux)),
		// サーバーがリクエストヘッダとアイドル keep-alive 接続を待つ時間を制限し、
		// 遅いヘッダ（Slowloris）クライアントが接続を無期限に保持できないようにする。
		// WriteTimeout は意図的に設定しない。/api/logs/stream はグローバルな書き込み
		// 期限が切断してしまう長時間 SSE 応答だからだ。WebSocket 接続は conn を
		// ハイジャックし、これらの値の影響を受けない。
		// Bound how long the server waits for request headers and for an idle
		// keep-alive connection, so a slow-header (Slowloris) client cannot
		// hold connections open indefinitely. WriteTimeout is intentionally
		// NOT set: /api/logs/stream is a long-lived SSE response that a global
		// write deadline would sever. WebSocket connections hijack the conn
		// and are likewise unaffected by these values.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		// リクエストヘッダを明示的に制限する。Go の既定は 1 MiB で、このアプリの
		// リクエストが必要とする量（大きな cookie も巨大な Authorization ヘッダも
		// 無い）よりはるかに多い。64 KiB は接続ごとのメモリを制限し、過大なヘッダ
		// リクエストを 1 MiB バッファへ読むのではなく 431 で即座に失敗させる。
		// Cap request headers explicitly. Go's default is 1 MiB, which is far
		// more than this app's requests need (no large cookies / no huge
		// Authorization header); 64 KiB bounds memory per connection and makes
		// an oversized-header request fail fast with 431 instead of being read
		// into a 1 MiB buffer.
		MaxHeaderBytes: 64 << 10, // 64 KiB
	}

	go func() {
		<-ctx.Done()
		lg.InfoT("dash.shutdown_stopping")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			lg.ErrorT("dash.shutdown_error", err)
		}
	}()

	lg.InfoT("dash.server_start", cfg.ListenAddr)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		lg.FatalT("dash.server_start_failed", err)
	}
	lg.InfoT("dash.server_stop")

	// シャットダウン時、バッチ集約の窓に残っているアラートを送信する。
	// Flush しないと、停止直前の数秒に発生した通知が失われる。
	// On shutdown, send any alerts still in the batch window. Without the
	// flush, notifications that occurred in the last few seconds before
	// stopping are lost.
	notifierMgr.Flush()
}
