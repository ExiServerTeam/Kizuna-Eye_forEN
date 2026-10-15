package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/internal/pluginsig"
	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/fsutil"
	kelog "Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/module"
)

// runGuardTimeout はサーバー側の二重実行ガードの安全弁だ。
// backup_result が永遠に届かない場合（Agent クラッシュ、ネットワーク断）、
// この時間後に running フラグを解放し、プラグインが永遠にブロックされないようにする。
// runGuardTimeout is the safety valve for the server-side double-run guard.
// If a backup_result never arrives (Agent crash, network loss), the running
// flag is released after this duration so the plugin is not blocked forever.
const runGuardTimeout = 30 * time.Minute

// maxSignatureBytes はアップロードの分離署名パートの上限だ。Ed25519 署名は
// 64 バイトで、余裕のある上限はコメント/hex の余地を残しつつ、敵対的な
// 数メガバイトの「署名」を拒否する。
// maxSignatureBytes caps the detached signature part of an upload. An Ed25519
// signature is 64 bytes; the generous cap leaves room for comments/hex, while
// still refusing a hostile multi-megabyte "signature".
const maxSignatureBytes = 8 << 10

// quarantineDirName は pluginsDir のサブディレクトリで、アップロードされた .so が
// 検証・検査される間待機する場所だ。pluginsDir 内にあるため最終的なライブパスへの
// rename が同一ファイルシステム（アトミック）になり、実行可能ファイルを含み得るため
// 0700 / 所有者のみだ。
// quarantineDirName is the sub-directory of pluginsDir where an uploaded .so
// waits while it is verified and inspected. It is inside pluginsDir so the
// final rename onto the live path is same-filesystem (atomic), and it is
// 0700/owner-only because it can contain an executable file.
const quarantineDirName = ".quarantine"

// ============================================================
// AgentHub は Agent へコマンドを送る。
// AgentHub sends commands to the Agent.
// ============================================================
type AgentHub interface {
	SendToAgent(payload map[string]interface{}) error
}

// ============================================================
// PluginManager はプラグインを管理する。
// PluginManager manages plugins.
// ============================================================
type PluginManager struct {
	storage       *ModulesStorage
	logger        module.Logger
	pluginsDir    string
	inspectBin    string
	hub           AgentHub
	uploadEnabled bool

	// A-3 ポリシー: アップロードされた .so の署名検証と、plugin-inspect の
	// 隔離実行。bwrapBin はログメッセージ用に事前解決し、実際の存在確認は
	// inspect の実行時に行う。
	// A-3 policy: signature verification of an uploaded .so and isolated
	// execution of plugin-inspect. bwrapBin is pre-resolved for the log
	// message; the actual existence check happens when inspect runs.
	policy   config.PluginSecurityConfig
	bwrapBin string

	// サーバー側の二重実行ガード。フロントエンドもボタンを無効化するが、
	// 別タブ/ブラウザまではカバーしないため、サーバーは対応する backup_result が
	// 届く（またはタイムアウトする）まで 2 回目の実行を拒否する。
	// Server-side double-run guard. The frontend also disables the button,
	// but that does not cover separate tabs/browsers, so the server refuses
	// a second run until the matching backup_result arrives (or times out).
	runMu   sync.Mutex
	running map[string]runGuard

	// audit を設定すると、セキュリティ関連のプラグイン操作（アップロード /
	// 削除）を受け取り、ログだけでなくアラート履歴にも記録する。
	// detail は日本語、detailEN は英語。
	// audit, when set, receives security-relevant plugin actions (upload /
	// delete) so they are recorded in the alert history, not only logged.
	// detail is Japanese, detailEN English.
	audit func(kind, detail, detailEN string)

	// pubKey は解析済み Ed25519 公開鍵をキャッシュする。loadPublicKey は最大 1 回
	// パースし、鍵ファイルは起動後読み取り専用だ。
	// pubKey caches the parsed Ed25519 public key. loadPublicKey parses it at
	// most once; the key file is read-only after startup.
	pubKeyOnce sync.Once
	pubKey     ed25519.PublicKey
	pubKeyErr  error
}

// loadPublicKey は設定されたプラグイン署名公開鍵を返し、鍵ファイルが未設定なら
// (nil, nil) を返す。結果はキャッシュされ、読み込み失敗もキャッシュして
// アップロードごとに再試行しないようにする。
// loadPublicKey returns the configured plugin-signing public key, or (nil,
// nil) when no key file is configured. The result is cached; a load failure
// is cached too so it is not retried on every upload.
func (p *PluginManager) loadPublicKey() (ed25519.PublicKey, error) {
	if strings.TrimSpace(p.policy.PublicKeyFile) == "" {
		return nil, nil
	}
	p.pubKeyOnce.Do(func() {
		p.pubKey, p.pubKeyErr = pluginsig.LoadPublicKey(p.policy.PublicKeyFile)
	})
	return p.pubKey, p.pubKeyErr
}

// SetAuditHook はプラグイン監査イベントのコールバックを設定する。detail は日本語
// レンダリング、detailEN は英語で、アラート履歴が両方を保存し UI を言語切替
// できるようにする。
// SetAuditHook installs the callback for plugin audit events. detail is the
// Japanese rendering, detailEN the English one; the alert history stores both
// so the UI can be switched between languages.
func (p *PluginManager) SetAuditHook(fn func(kind, detail, detailEN string)) {
	p.audit = fn
}

// reportAudit は監査フックがあれば呼ぶ。
// reportAudit invokes the audit hook, if any.
func (p *PluginManager) reportAudit(kind, detail, detailEN string) {
	if p.audit != nil {
		p.audit(kind, detail, detailEN)
	}
}

// runGuard は実行中の手動実行を記録し、その backup_result を request_id で
// マッチできるようにする。マッチにより、無関係な（またはスケジュールされた）
// 結果がガードを解放し、1 回目がまだ実行中に 2 回目を開始させることを防ぐ。
// runGuard records an in-flight manual run so its backup_result can be matched
// by request_id. Matching prevents an unrelated (or scheduled) result from
// releasing the guard and letting a second run start while the first is still
// executing.
type runGuard struct {
	requestID string
	startedAt time.Time
}

// PluginMeta は plugins/{name}.meta.json の内容だ。
// PluginMeta is the content of plugins/{name}.meta.json.
type PluginMeta struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	// Tag はプラグインが Tag() string で宣言する短いバッジだ
	// （"GUARD" / "LITE" / "PRO" ...）。空はプラグインが宣言しておらず、
	// UI が "PLUGIN" を表示することを意味する。
	// Tag is the short badge the plugin declares via Tag() string
	// ("GUARD" / "LITE" / "PRO" ...). Empty means the plugin did not
	// declare one and the UI shows "PLUGIN".
	Tag         string               `json:"tag,omitempty"`
	SOFilename  string               `json:"so_filename"`
	Description string               `json:"description,omitempty"`
	IntervalSec float64              `json:"interval_sec,omitempty"`
	Fields      []module.ConfigField `json:"fields"`
	IsBackup    bool                 `json:"is_backup"`
	// DashboardCards はモジュールが DashboardCards() で宣言したもの
	// （dashboard に提供するカード）だ。空はモジュールがカードを提供しないことを
	// 意味する。フロントエンドはこれを使ってカードを追加/削除するため、カードは
	// コアにハードコードされるのではなくモジュールが所有する。
	// DashboardCards is what the module declared via DashboardCards()
	// (the card it contributes to the dashboard). Empty means the module
	// contributes no card. The frontend uses it to add/remove cards, so the
	// card is owned by the module, not hardcoded in the core.
	DashboardCards []module.DashboardCard `json:"dashboard_cards,omitempty"`
	UploadedAt     string                 `json:"uploaded_at"`
	UploaderIP     string                 `json:"uploader_ip,omitempty"`
	InspectError   string                 `json:"inspect_error,omitempty"`
	HasWebUI       bool                   `json:"has_web_ui,omitempty"`
	// WebUIPath はモジュール自身の Web UI のエントリ文書（UI ルートからの相対）で、
	// モジュールが WebAssetProvider で同梱する場合に使う。dashboard はそれを
	// （/plugins/<name>/ から配信される iframe 内で）モジュール提供カードとして
	// マウントするため、カード内容はモジュールが所有する。
	// WebUIPath is the entry document of the module's own web UI (relative
	// to the UI root), when the module ships one via WebAssetProvider. The
	// dashboard mounts it (in an iframe served from /plugins/<name>/) as the
	// module-provided card, so the card content is owned by the module.
	WebUIPath string `json:"web_ui_path,omitempty"`
	// Origin は、アップロードが設定済み公開鍵に対して検証された署名を持てば
	// "official"、そうでなければ "third_party" だ（未署名アップロードは
	// 明示的な確認後のみ許可される）。UI はこれを使って純正 Kizuna プラグインと
	// サードパーティを区別する。
	// Origin is "official" when the upload carried a signature that
	// verified against the configured public key, and "third_party"
	// otherwise (unsigned upload, allowed only after explicit
	// confirmation). The UI uses this to distinguish pure Kizuna plugins
	// from third-party ones.
	Origin string `json:"origin,omitempty"`
	// UserLabel はアップロード時に運用者が入力した名前だ。表示専用で、
	// modules.json と agent レジストリはプラグイン自身の Name() をキーに保つが、
	// カードは UserLabel を表示し、運用者が選んだ名前を見られるようにする。空は
	// アップロード名がプラグイン自身の名前と一致し、余分に表示するものが無いことを
	// 意味する。
	// UserLabel is the name the operator typed at upload time. It is
	// display-only: modules.json and the agent registry stay keyed by the
	// plugin's own Name(), but the card shows UserLabel so the operator
	// sees the name they chose. Empty means the upload name matched the
	// plugin's own name, so there is nothing extra to show.
	UserLabel string `json:"user_label,omitempty"`
}

// NewPluginManager は PluginManager を作る。
// hub は Agent へコマンドを送る。nil は手動実行を無効化する。
// NewPluginManager creates a PluginManager.
// hub sends commands to the Agent; nil disables manual runs.
func NewPluginManager(storage *ModulesStorage, cfg *config.DashboardConfig, logger module.Logger, hub AgentHub) (*PluginManager, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("実行ファイルパス取得失敗: %w", err)
	}
	baseDir := filepath.Dir(exePath)

	// 設定からプラグインディレクトリを解決する（フォールバック: 実行ファイル隣の plugins/）。
	// Resolve the plugins directory from config (fallback: plugins/ next to the executable).
	pluginsDir := filepath.Join(baseDir, "plugins")
	uploadEnabled := false
	if cfg != nil {
		if dir, err := cfg.EnsurePluginsDir(); err == nil {
			pluginsDir = dir
		}
		uploadEnabled = cfg.IsUploadEnabled()
	}
	// このディレクトリは実行可能な .so を保持する。所有者のみ書き込み可能に保ち
	// （dashboard ユーザーだけがプラグインを配置できる）、agent が読み取り・通過
	// できるようにする。A-4 で agent は別ユーザー（kizuna-eye）で動くため、
	// 0700 ディレクトリからはプラグインをロードできない。
	// The directory holds executable .so files. It must stay owner-writable
	// (only the dashboard user may place plugins) while remaining readable and
	// traversable by the agent: with A-4 the agent runs as a separate user
	// (kizuna-eye) and could not load plugins out of a 0700 directory.
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return nil, fmt.Errorf("plugins ディレクトリ作成失敗: %w", err)
	}

	// A-3: 保留中アップロード用の検疫ディレクトリ。
	// A-3: quarantine directory for pending uploads.
	quarantineDir := filepath.Join(pluginsDir, quarantineDirName)
	if err := os.MkdirAll(quarantineDir, 0o700); err != nil {
		return nil, fmt.Errorf("検疫ディレクトリ作成失敗 (%s): %w", quarantineDir, err)
	}

	inspectBin := filepath.Join(baseDir, "plugin-inspect")
	if _, err := os.Stat(inspectBin); err != nil {
		return nil, fmt.Errorf("plugin-inspect が見つかりません: %s (%w)", inspectBin, err)
	}

	// A-3 ポリシー。使用可能な鍵無しの署名要求は設定エラーで、最初の
	// アップロード時ではなく起動時に捕まえなければならない。
	// A-3 policy. A signature requirement without a usable key is a config
	// error that must be caught at startup, not at the first upload.
	var policy config.PluginSecurityConfig
	if cfg != nil {
		policy = cfg.Plugins
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.WantSignature() {
		if _, err := pluginsig.LoadPublicKey(policy.PublicKeyFile); err != nil {
			return nil, fmt.Errorf("plugins.public_key_file を読み込めません: %w", err)
		}
	}
	bwrapBin := ""
	if policy.WantInspectIsolation() {
		bwrapBin = policy.BwrapBinary()
		if _, err := os.Stat(bwrapBin); err != nil && logger != nil {
			// ここでは致命ではない。確認は実際にアップロードを検査するときに
			// fail-closed で強制される。設定ミスを最初のアップロード時だけでなく
			// 起動時に可視化するため警告する。
			// Not fatal here: the check is enforced fail-closed when an
			// upload is actually inspected. Warn so the misconfiguration is
			// visible at startup rather than only on the first upload.
			kelog.LogError(logger, "api.bwrap_missing", bwrapBin, err)
		}
	}

	return &PluginManager{
		storage:       storage,
		logger:        logger,
		pluginsDir:    pluginsDir,
		inspectBin:    inspectBin,
		hub:           hub,
		uploadEnabled: uploadEnabled,
		policy:        policy,
		bwrapBin:      bwrapBin,
		running:       make(map[string]runGuard),
	}, nil
}

// ensureQuarantineDir は検疫ディレクトリを返し、必要なら作成する。
// テストで直接組み立てられた PluginManager 値は NewPluginManager を通らないため、
// ディレクトリは遅延作成もする。
// ensureQuarantineDir returns the quarantine directory, creating it if needed.
// The directory is created lazily as well because PluginManager values built
// directly in tests do not pass through NewPluginManager.
func (p *PluginManager) ensureQuarantineDir() (string, error) {
	dir := filepath.Join(p.pluginsDir, quarantineDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("%s: %w", dir, err)
	}
	return dir, nil
}

// RegisterRoutes はプラグインのルートを登録する。
// RegisterRoutes registers the plugin routes.
func (p *PluginManager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/plugins/upload", p.handleUpload)
	mux.HandleFunc("GET /api/plugins", p.handleList)
	mux.HandleFunc("DELETE /api/plugins/{name}", p.handleDelete)
	mux.HandleFunc("POST /api/plugins/{name}/run", p.handleRunPlugin) // ★ Phase 8-5
	mux.HandleFunc("PUT /api/plugins/{name}/label", p.handleRenameLabel)
	// plugins/<name>.web/ からプラグイン Web UI を配信する。
	// Serve plugin web UI from plugins/<name>.web/.
	mux.HandleFunc("GET /plugins/{name}/", p.handleWebUI)
}

// handleWebUI はプラグイン同梱の Web UI を配信する。
//
// 設計書 5.4 に従い、実行方式を origin で分ける:
//   - 純正 (official): 直接配信する。
//   - サードパーティ (origin=third_party): sandbox iframe で隔離する。
//     /plugins/{name}/ はラッパー HTML を返し、実コンテンツは
//     /plugins/{name}/_asset/... から読み込む。sandbox に allow-same-origin を
//     付けないため、iframe 内は opaque origin になり、親の cookie / 本体 API /
//     DOM にアクセスできない。加えて実コンテンツ側 CSP で connect-src を
//     禁止し、UI からの API 呼び出しを二重に塞ぐ。
//
// handleWebUI serves a plugin's bundled web UI.
//
// 設計書 5.4 に従い、実行方式を origin で分ける:
//   - 純正 (official): 直接配信する。
//   - サードパーティ (origin=third_party): sandbox iframe で隔離する。
//     /plugins/{name}/ はラッパー HTML を返し、実コンテンツは
//     /plugins/{name}/_asset/... から読み込む。sandbox に allow-same-origin を
//     付けないため、iframe 内は opaque origin になり、親の cookie / 本体 API /
//     DOM にアクセスできない。加えて実コンテンツ側 CSP で connect-src を
//     禁止し、UI からの API 呼び出しを二重に塞ぐ。
func (p *PluginManager) handleWebUI(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := sanitizeName(name); err != nil {
		http.NotFound(w, r)
		return
	}

	baseDir := filepath.Join(p.pluginsDir, name+".web")
	info, err := os.Stat(baseDir)
	if err != nil || !info.IsDir() {
		http.NotFound(w, r)
		return
	}

	// 純正と証明できたときだけ直接配信する。それ以外はサンドボックス。
	// Serve directly only when proven official; otherwise sandbox.
	thirdParty := !p.isOfficialPlugin(name)

	// /plugins/{name}/. の後のパスを解決する。
	// Resolve the path after /plugins/{name}/.
	rel := strings.TrimPrefix(r.URL.Path, "/plugins/"+name+"/")

	if thirdParty {
		// _asset/ 以外（トップや index.html 直指定）はラッパーを返す。
		// これにより実体ファイルは sandbox iframe 経由でしか取得できない
		// （/plugins/<name>/index.html を直接開いてもラッパーが返る）。
		// Anything but _asset/ (the top or a direct index.html) returns the
		// wrapper, so the real files can only be fetched through the sandbox
		// iframe (opening /plugins/<name>/index.html directly still returns
		// the wrapper).
		if !strings.HasPrefix(rel, "_asset/") {
			// dashboard の ?lang=<ui> をラッパーへ転送し、内側の iframe が
			// 現在の言語で描画できるようにする（外側リクエストのクエリは
			// そうしないと失われる）。値は serveSandboxWrapper が
			// URL/HTML へ埋め込む前に ja/en へ検証する。
			// Forward the dashboard's ?lang=<ui> to the wrapper so the inner
			// iframe can render in the current language (the outer request's
			// query is otherwise lost). The value is validated to ja/en in
			// serveSandboxWrapper before it is embedded in the URL/HTML.
			p.serveSandboxWrapper(w, name, r.URL.Query().Get("lang"))
			return
		}
		// 実体は plugin-inspect --web-out が WebAssets() の相対パスを
		// baseDir 直下へ展開する。ラッパーは同一オリジン制約と sandbox の
		// 都合で _asset/ 経由で参照するため、ここで接頭辞を剥がして実際の
		// ファイル（baseDir/<rel>）へ解決する。剥がさないと baseDir/_asset/
		// <rel> を探して必ず 404 になり、サードパーティの Web UI が
		// 何も表示されない（設計書 5.4 の隔離 UI が機能しない）。
		// The assets are extracted by plugin-inspect --web-out under baseDir
		// using the relative paths from WebAssets(). The wrapper references
		// them via _asset/ (same-origin constraints and sandbox), so strip
		// the prefix here to resolve the real file (baseDir/<rel>). Without
		// this, baseDir/_asset/<rel> would always 404 and a third-party web
		// UI would show nothing (the isolated UI of design 5.4 would not
		// work).
		rel = strings.TrimPrefix(rel, "_asset/")
	}
	if rel == "" {
		rel = "index.html"
	}

	// パストラバーサルを防ぐ。
	// Prevent path traversal.
	cleanRel := filepath.Clean("/" + rel)
	fullPath := filepath.Join(baseDir, cleanRel)
	if !strings.HasPrefix(fullPath, baseDir+string(os.PathSeparator)) && fullPath != baseDir {
		http.NotFound(w, r)
		return
	}

	// ディレクトリ要求には index.html を付ける。
	// Append index.html for directory requests.
	if st, err := os.Stat(fullPath); err == nil && st.IsDir() {
		fullPath = filepath.Join(fullPath, "index.html")
	}

	// シンボリックリンクを解決し、包含を再確認する。上の字句 HasPrefix は
	// シンボリックリンクを辿らない。Web UI ディレクトリ内のシンボリックリンク
	// （例: link.txt -> /etc/passwd）はプレフィックスチェックを通過するが、
	// ServeFile は対象の内容を送ってしまう。プラグイン .web ディレクトリは
	// agent ユーザーが書き込めるため、侵害されたプラグインがそのようなリンクを
	// 仕込み、agent が読める任意ファイルを読める。EvalSymlinks が実パスを解決し、
	// 実ファイルが依然 baseDir 内にある場合のみ配信する。
	// Resolve symlinks and re-check containment. The lexical HasPrefix above
	// does not follow symlinks: a symlink inside the web UI dir (e.g.
	// link.txt -> /etc/passwd) passes the prefix check but ServeFile would
	// send the target's contents. A plugin .web dir is writable by the agent
	// user, so a compromised plugin could plant such a link and read any file
	// the agent can read. EvalSymlinks resolves the real path; the request is
	// only served when the real file is still inside baseDir.
	realBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	realPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !strings.HasPrefix(realPath, realBase+string(os.PathSeparator)) && realPath != realBase {
		http.NotFound(w, r)
		return
	}
	// 通常ファイルでないもの（デバイス、ソケット、ディレクトリ）を拒否する。
	// Reject anything that is not a regular file (devices, sockets, dirs).
	if st, err := os.Stat(realPath); err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	h := w.Header()
	// Content-Type は下の拡張子から導出する（CSS/JS/フォントを誤ラベルしては
	// ならない。nosniff で誤った型はアセットを壊す）。
	// Content-Type is derived from the file extension below (CSS/JS/fonts
	// must not be mislabelled: with nosniff a wrong type breaks the asset).
	h.Set("X-Content-Type-Options", "nosniff")
	// X-Frame-Options: サードパーティ UI はラッパーの sandbox iframe 内で
	// 表示するため、DENY だとブラウザが埋め込みを拒否して何も出ない。
	// 埋め込み許可は CSP の frame-ancestors で制御する（サードパーティは
	// 'self' のみ、純正は 'none'）。XFO は CSP 非対応の古いブラウザ向けの
	// 補助で、サードパーティは SAMEORIGIN、純正は DENY とする。
	// Both origins are embedded by the dashboard as the module-provided
	// card (design 5.4: the core provides a generic iframe host). Only the
	// same origin may frame them; a cross-origin page cannot (clickjacking
	// protection). Denying framing outright (DENY / frame-ancestors 'none')
	// made the card render blank for every module.
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "no-referrer")
	if thirdParty {
		// サードパーティ UI: 本体 API へのアクセスを禁止する（多層防御）。
		// Third-party UI: forbid access to the core API (defense in depth).
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"connect-src 'none'; "+
				"frame-ancestors 'self'; "+
				"base-uri 'none'; "+
				"form-action 'none'")
	} else {
		// 純正 UI: 本体と同水準（script は 'self' のみ）。埋め込みは
		// 同一オリジン（ダッシュボードのカード枠）だけを許可する。
		// Official UI: same level as the core (script 'self' only). Framing
		// is allowed only from the same origin (the dashboard card slot).
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'self'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
	}

	// ServeFile ではなく ServeContent で配信する。ServeFile は URL パスが
	// "/index.html" で終わると同じパスからそれを除いたものへ 301 リダイレクト
	// する特別扱いがある。sandbox ラッパーの iframe src はまさに
	// "/plugins/<name>/_asset/index.html" なので、ServeFile はサードパーティ UI の
	// ロードごとに 301 を返していた。既に検証済みの通常ファイルを直接配信すれば、
	// 上のシンボリックリンク/包含チェックを保ちつつそのリダイレクトを避けられる。
	// Serve with ServeContent instead of ServeFile: ServeFile has a special
	// case that 301-redirects any request whose URL path ends in
	// "/index.html" to the same path without it. The sandbox wrapper's iframe
	// src is exactly "/plugins/<name>/_asset/index.html", so ServeFile
	// answered every third-party UI load with a 301. Serving the already
	// validated regular file directly avoids that redirect while keeping the
	// symlink/containment checks above.
	f, ferr := os.Open(realPath)
	if ferr != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, serr := f.Stat()
	if serr != nil {
		http.NotFound(w, r)
		return
	}
	// 拡張子から型を設定する。Go が知らない拡張子（モジュール独自アセット）は
	// text/html へフォールバックし、エントリ文書が依然描画されるようにする。
	// nosniff によりブラウザは実行し得る型を推測するのではなく HTML として扱う。
	// Set the type from the extension. For an extension Go does not know (a
	// module's custom asset), fall back to text/html so the entry document
	// still renders; nosniff then makes the browser treat it as HTML rather
	// than guessing a type that could execute.
	ct := mime.TypeByExtension(filepath.Ext(realPath))
	if ct == "" {
		ct = "text/html; charset=utf-8"
	}
	h.Set("Content-Type", ct)
	http.ServeContent(w, r, filepath.Base(realPath), fi.ModTime(), f)
}

// serveSandboxWrapper はサードパーティのプラグイン Web UI 用の sandbox-iframe
// ラッパーを書く。iframe は allow-same-origin を「付けない」sandbox を使うため、
// その内容は opaque origin で動作し、親の cookie を読めず、dashboard API を
// 呼べず、親 DOM に触れない。/plugins/{name}/_asset/ 下のアセットだけをロードする。
// serveSandboxWrapper writes the sandbox-iframe wrapper for a third-party
// plugin web UI. The iframe uses sandbox WITHOUT allow-same-origin, so its
// content runs in an opaque origin: it cannot read the parent's cookies, call
// the dashboard API, or touch the parent DOM. Only the assets under
// /plugins/{name}/_asset/ are loaded.
func (p *PluginManager) serveSandboxWrapper(w http.ResponseWriter, name, rawLang string) {
	// name は sanitizeName() で確認済み（[A-Za-z0-9_-]）で、URL に埋め込んで安全だ。
	// モジュールが WebUIPath() で宣言したエントリ（meta.json に web_ui_path として
	// 記録される）を使い、ハードコードの index.html は使わない。モジュールが
	// エントリを別名にし得るため、宣言を無視すると正当な UI が 404 になる
	// （設計 5.4: UI はモジュールが所有する）。p.safeWebUIEntry() は絶対パス、
	// ".."、バックスラッシュを拒否するため、値は _asset/ 内に留まる。
	// name is sanitizeName()-checked ([A-Za-z0-9_-]); safe to embed in the URL.
	// Use the entry the module declared via WebUIPath() (recorded in meta.json
	// as web_ui_path), not a hardcoded index.html: a module may name its entry
	// differently, and ignoring the declaration would 404 a valid UI (design
	// 5.4: the module owns its UI). p.safeWebUIEntry() rejects absolute paths,
	// ".." and backslashes, so the value stays inside _asset/.
	entry := p.safeWebUIEntry(name)
	// ja/en のみを受け付けるため、値は URL と html lang 属性へ埋め込んで安全だ
	// （注入なし、任意クエリのパススルーなし）。
	// Only ja/en are accepted, so the value is safe to embed in the URL and the
	// html lang attribute (no injection, no arbitrary query pass-through).
	lang := "ja"
	switch strings.ToLower(strings.TrimSpace(rawLang)) {
	case "en":
		lang = "en"
	case "ja":
		lang = "ja"
	}
	// html.EscapeString は多層防御だ。safeWebUIEntry は既に引用符/山括弧を
	// 拒否するが、値は dashboard 自身のオリジンから配信されるページの属性へ
	// 書き込まれるため、将来の変更が entry 検証を緩めても属性から脱出できない
	// ようにしなければならない。
	// html.EscapeString is defense in depth: safeWebUIEntry already rejects
	// quotes/angle brackets, but the value is written into an attribute of a
	// page served from the dashboard's own origin, so it must never be able
	// to break out of the attribute even if a future change loosens the
	// entry validation.
	src := "/plugins/" + name + "/_asset/" + html.EscapeString(entry) + "?lang=" + lang
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	// dashboard はこのラッパーを、モジュール提供カードとして同一オリジンの
	// iframe に埋め込む。X-Frame-Options: DENY（および明示的な frame-ancestors の
	// 無い CSP は default-src 'none' = 'none' へフォールバックする）だとブラウザが
	// その埋め込みを拒否し、カードが空白になる。同一オリジンのみを許可する。
	// ラッパーは秘密を持たず、モジュールの実 UI は内側の sandbox iframe で隔離される。
	// The dashboard embeds this wrapper in a same-origin iframe as the
	// module-provided card. X-Frame-Options: DENY (and a CSP without an
	// explicit frame-ancestors, which falls back to default-src 'none' =
	// 'none') makes the browser refuse that embedding, so the card renders
	// blank. Allow only the same origin; the wrapper holds no secrets, and
	// the module's real UI is isolated by the inner sandboxed iframe.
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy",
		"default-src 'none'; frame-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'self'")
	page := "<!DOCTYPE html>\n<html lang=\"" + lang + "\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n" +
		"<title>Plugin UI</title>\n" +
		"<style>html,body{margin:0;height:100%;background:transparent}" +
		"iframe{border:0;width:100%;height:100%;display:block}</style>\n" +
		"</head>\n<body>\n" +
		"<iframe sandbox=\"allow-scripts allow-forms allow-popups allow-modals allow-downloads\" " +
		"referrerpolicy=\"no-referrer\" src=\"" + src + "\"></iframe>\n" +
		"</body>\n</html>\n"
	_, _ = io.WriteString(w, page)
}

// safeWebUIEntry はモジュールが宣言したエントリ文書（meta.json の web_ui_path）を
// UI ルートからの相対の安全なパスとして返し、既定は "index.html" だ。
// アセット展開側と同じトラバーサル規則を使うため、敵対的な meta が sandbox
// ラッパーに _asset/ の外をロードさせることができない。
// safeWebUIEntry returns the module's declared entry document (meta.json's
// web_ui_path) as a safe path relative to the UI root, defaulting to
// "index.html". It reuses the same traversal rules as the asset extractor, so
// a hostile meta cannot make the sandbox wrapper load outside _asset/.
func (p *PluginManager) safeWebUIEntry(name string) string {
	const fallback = "index.html"
	data, err := os.ReadFile(filepath.Join(p.pluginsDir, name+".meta.json"))
	if err != nil {
		return fallback
	}
	var m PluginMeta
	if json.Unmarshal(data, &m) != nil || m.WebUIPath == "" {
		return fallback
	}
	p2 := strings.TrimSpace(m.WebUIPath)
	if strings.HasPrefix(p2, "/") || strings.Contains(p2, "\\") {
		return fallback
	}
	clean := path.Clean(p2)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fallback
	}
	// HTML メタ文字を拒否する。エントリは sandbox ラッパーの
	// <iframe src="..."> 属性（serveSandboxWrapper）に埋め込まれ、引用符があると
	// 敵対的なモジュールが属性を閉じて dashboard 自身のオリジンへ onload= ハンドラを
	// 注入し得る（細工された .so 経由の保存型 XSS）。トラバーサルは上で既に
	// ブロック済みで、これがマークアップ注入の角度も塞ぐ。fail-closed で
	// index.html へ。
	// Reject HTML metacharacters. The entry is embedded in the sandbox
	// wrapper's <iframe src="..."> attribute (serveSandboxWrapper), and a
	// quote would let a hostile module close the attribute and inject an
	// onload= handler into the dashboard's own origin (stored XSS via a
	// crafted .so). Traversal is already blocked above; this closes the
	// markup-injection angle as well. Fail-closed to index.html.
	if strings.ContainsAny(clean, "<>\"'&`") {
		return fallback
	}
	return clean
}

// isOfficialPlugin はプラグインの meta.json が origin=official を証明するかを返す。
//
// fail-closed: 純正であると「証明できた」ときだけ true を返す。meta.json が
// 無い・読めない・origin が空/third_party のいずれの場合も false とし、
// サンドボックス（third-party 扱い）で配信する。逆にすると、meta を欠落
// させただけでサードパーティ UI がサンドボックス無しで直接配信され、
// 本体 API を叩けてしまう（設計書 5.4 違反）。
// isOfficialPlugin reports whether a plugin's meta.json proves origin=official.
//
// fail-closed: 純正であると「証明できた」ときだけ true を返す。meta.json が
// 無い・読めない・origin が空/third_party のいずれの場合も false とし、
// サンドボックス（third-party 扱い）で配信する。逆にすると、meta を欠落
// させただけでサードパーティ UI がサンドボックス無しで直接配信され、
// 本体 API を叩けてしまう（設計書 5.4 違反）。
func (p *PluginManager) isOfficialPlugin(name string) bool {
	data, err := os.ReadFile(filepath.Join(p.pluginsDir, name+".meta.json"))
	if err != nil {
		return false
	}
	var m PluginMeta
	if json.Unmarshal(data, &m) != nil {
		return false
	}
	return m.Origin == "official"
}

// reconcileWebUIDir はプラグインの展開済み Web UI を、アップロード名の
// ディレクトリから正規名のディレクトリへ移動する。これにより、ファイル名が
// プラグイン自身の Name() と異なるアップロード後も /plugins/<canonical>/ と
// HasWebUI フラグ（どちらも meta.Name 由来）が解決する。これが無いと iframe が
// 404 になり、孤立した <upload>.web ディレクトリが削除時（削除は <canonical>.web のみ
// を除去する）に決して清掃されない。ベストエフォートで、いかなる失敗でも
// 呼び出し側は続行する。カード欠落はセキュリティやロードの問題ではないためだ。
// reconcileWebUIDir moves a plugin's extracted web UI from the upload-name
// directory to the canonical-name directory, so /plugins/<canonical>/ (and the
// HasWebUI flag, both derived from meta.Name) resolve after an upload whose
// file name differs from the plugin's own Name(). Without this the iframe
// 404s and the orphan <upload>.web directory is never cleaned up on delete
// (delete removes <canonical>.web only). Best-effort: on any failure the
// caller proceeds, because a missing card is not a security or load problem.
func (p *PluginManager) reconcileWebUIDir(fromName, toName string) {
	if fromName == "" || toName == "" || fromName == toName {
		return
	}
	src := filepath.Join(p.pluginsDir, fromName+".web")
	dst := filepath.Join(p.pluginsDir, toName+".web")
	if _, err := os.Stat(src); err != nil {
		return // Web UI が展開されなかった / no web UI was extracted
	}
	if _, err := os.Stat(dst); err == nil {
		return // 正規名のディレクトリが既に存在する / a directory for the canonical name already exists
	}
	if err := os.Rename(src, dst); err != nil && p.logger != nil {
		kelog.LogWarn(p.logger, "api.plugin_webdir_rename_failed", fromName, toName, err)
	}
}

// fileExists は path が存在するか（任意の型）を返す。
// fileExists reports whether path exists (any type).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var safeNameRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)

// remoteIP は "host:port" の RemoteAddr からポートを落としてクライアント IP を返し、
// アラートメッセージが読みやすくなるようにする。host:port でない値はそのまま返す。
// remoteIP returns the client IP from a "host:port" RemoteAddr, dropping
// the port so alert messages read cleanly. A value that is not host:port is
// returned unchanged.
func remoteIP(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// sanitizeLabel はユーザー提供の表示ラベルを清掃する。sanitizeName と違い
// 任意の UTF-8 を受け付ける（"My Backup 2026" や "バックアップ" が動く）。
// ラベルは表示専用で、パスや識別子には決してならないためだ。制御文字を除去し、
// ルーン数を 64 に制限する。
// sanitizeLabel cleans a user-supplied display label. Unlike sanitizeName it
// accepts any UTF-8 (so "My Backup 2026" or "バックアップ" works), because the
// label is display-only and never becomes a path or an identifier. Control
// characters are stripped and the rune count is capped at 64.
func sanitizeLabel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	s = strings.TrimSpace(b.String())
	if len([]rune(s)) > 64 {
		return "", fmt.Errorf("プラグインの表示名が長すぎます（64文字以内で指定してください）")
	}
	return s, nil
}

func sanitizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("名前が空です")
	}
	if !safeNameRe.MatchString(name) {
		return "", fmt.Errorf("名前に使用できるのは英数字・ハイフン・アンダースコアのみです")
	}
	if len(name) > 64 {
		return "", fmt.Errorf("名前が長すぎます（64文字以内）")
	}
	return name, nil
}

// ============================================================
// 手動実行。
// Manual run.
// ============================================================
func (p *PluginManager) handleRunPlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "プラグイン名が指定されていません")
		return
	}
	if _, err := sanitizeName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if p.hub == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Agent ハブが未設定です")
		return
	}

	// 手動実行できるのはバックアップ対応プラグインだけだ。監視系プラグイン
	// （例: security）は実行要求を受け付けてはならない。そうしないと Agent が
	// クリックのたびにエラーを返す。
	// Only backup-capable plugins can be run manually. Watching plugins
	// (e.g. security) must not accept a run request; otherwise the Agent
	// would return an error for every click.
	if !p.isBackupPlugin(name) {
		writeJSONError(w, http.StatusBadRequest, "このプラグインは手動実行に対応していません。")
		return
	}

	// 1 回目が進行中の間は 2 回目の実行を拒否する。
	// Reject a second run while the first is still in progress.
	requestID, ok := p.acquireRun(name)
	if !ok {
		writeJSONError(w, http.StatusConflict, "このプラグインは実行中です。完了までお待ちください。")
		return
	}

	if err := p.hub.SendToAgent(map[string]interface{}{
		"action":     "run_backup",
		"plugin":     name,
		"request_id": requestID,
	}); err != nil {
		// 要求は Agent へ届かなかった。ガードを即座に解放する。
		// The request never reached the Agent; release the guard immediately.
		p.releaseRun(name, requestID)
		writeJSONError(w, http.StatusServiceUnavailable, "Agent に接続できません: "+err.Error())
		return
	}

	if p.logger != nil {
		kelog.LogInfo(p.logger, "api.plugin_run_request", name, requestID)
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "accepted",
		"request_id": requestID,
		"plugin":     name,
	})
}

// isBackupPlugin はプラグインの meta が is_backup=true を宣言しているかを返す。
// 未知のプラグイン（meta 無し）は false を返し、実行可能にしない。
// isBackupPlugin reports whether the plugin's meta declares is_backup=true.
// Unknown plugins (no meta) return false so they are not runnable.
func (p *PluginManager) isBackupPlugin(name string) bool {
	metaPath := filepath.Join(p.pluginsDir, name+".meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	var meta PluginMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	return meta.IsBackup
}

// acquireRun はプラグインを実行中としてマークし、Agent へ送る request_id を返す。
// プラグインが既に実行中で前回の実行がまだタイムアウトしていない場合、ok は false だ。
// acquireRun marks a plugin as running and returns the request_id to send to
// the Agent. ok is false when the plugin is already running and the previous
// run has not timed out yet.
func (p *PluginManager) acquireRun(name string) (requestID string, ok bool) {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	if g, exists := p.running[name]; exists {
		if time.Since(g.startedAt) < runGuardTimeout {
			return "", false
		}
		// 古いエントリ: 結果が戻らなかった。再試行を許可する。
		// Stale entry: the result never came back. Allow a retry.
	}
	requestID = generateRequestID()
	p.running[name] = runGuard{requestID: requestID, startedAt: time.Now()}
	return requestID, true
}

// releaseRun はプラグインの実行中フラグをクリアするが、requestID が実行中のものと
// 一致する場合のみだ。空または不一致の request_id は何もしないため、スケジュール
// 実行の結果（request_id を運ばない）や別リクエストの結果が手動実行のガードを
// 解放できない。
// releaseRun clears the running flag for a plugin, but only when requestID
// matches the in-flight run. An empty or mismatched request_id does nothing,
// so a scheduled run's result (which carries no request_id) or another
// request's result cannot release a manual run's guard.
func (p *PluginManager) releaseRun(name, requestID string) {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	g, ok := p.running[name]
	if !ok {
		return
	}
	if requestID == "" || g.requestID != requestID {
		return
	}
	delete(p.running, name)
}

// HandleAgentEvent は Agent からの全イベントメッセージに対して hub が呼ぶ。
// backup_result が届くと実行ガードを解放し、プラグインを再実行できるようにする。
// HandleAgentEvent is called by the hub for every event message from the Agent.
// It releases the run guard when a backup_result arrives so the plugin can be
// run again.
func (p *PluginManager) HandleAgentEvent(msg []byte) {
	var env struct {
		Event     string `json:"event"`
		Plugin    string `json:"plugin"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		return
	}
	if env.Event != "backup_result" || env.Plugin == "" {
		return
	}
	p.releaseRun(env.Plugin, env.RequestID)
}

// generateRequestID はランダムな 16 バイト ID を返す。
// generateRequestID returns a random 16-byte ID.
func generateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

// ============================================================
// handleUpload はアップロードされた .so を保存し、plugin-inspect でメタデータを展開する。
// handleUpload stores an uploaded .so and extracts metadata with plugin-inspect.
// ============================================================
func (p *PluginManager) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !p.uploadEnabled {
		writeJSONError(w, http.StatusForbidden, "プラグインのアップロードは無効です（dashboard_config.json の plugins_upload_enabled を true にしてください）")
		return
	}

	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "ファイル解析エラー: "+err.Error())
		return
	}

	rawLabel := r.FormValue("name")
	file, header, err := r.FormFile("plugin")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "ファイルがありません: "+err.Error())
		return
	}
	defer file.Close()

	if !strings.HasSuffix(header.Filename, ".so") {
		writeJSONError(w, http.StatusBadRequest, "拡張子は .so である必要があります")
		return
	}
	// 入力された名前は表示専用ラベルだ。ファイルシステム安全な名前は代わりに
	// アップロードファイル名から導出し、日本語や空白入りラベルが検疫一時ファイルを
	// 壊したり modules.json のキーになったりしないようにする。プラグイン自身の
	// Name() が下で正規識別子としてこれを置き換える。
	// The typed name is a display-only label. Derive the filesystem-safe name
	// from the uploaded filename instead, so a Japanese or spaced label cannot
	// break the quarantine temp file or become a modules.json key. The
	// plugin's own Name() replaces this as the canonical identifier below.
	fsBase := strings.TrimSuffix(filepath.Base(header.Filename), ".so")
	name, err := sanitizeName(fsBase)
	if err != nil {
		// ファイル名が安全な識別子でない。一意なプレースホルダを使う。
		// プラグイン自身の Name() が後で正規識別子になる。
		// Filename is not a safe identifier; use a unique placeholder. The
		// plugin's own Name() becomes the canonical identifier later anyway.
		name = "plugin_" + time.Now().Format("20060102_150405")
	}
	if rawLabel == "" {
		rawLabel = name
	}
	label, lerr := sanitizeLabel(rawLabel)
	if lerr != nil {
		writeJSONError(w, http.StatusBadRequest, lerr.Error())
		return
	}
	if label == "" {
		label = name
	}

	// soPath/metaPath は plugin-inspect の後に確定する。正規名はプラグイン自身から
	// 来る（下記参照）。
	// soPath/metaPath are finalized after plugin-inspect: the canonical
	// name comes from the plugin itself (see below).
	var soPath, metaPath string

	// 一意な一時ファイルを使い、同じプラグイン名の並行アップロード（または
	// クラッシュで残った古い .tmp）が衝突しないようにする。アップロードは
	// inspect + meta + modules.json がすべて成功するまで一時ファイルに留まるため、
	// 失敗が孤立した、実行可能かもしれない .so を残すことが決してない。
	// A-3: アップロードは検疫ディレクトリ（0700、pluginsDir 内で最終 rename が
	// 同一ファイルシステムかつアトミック）へ着地する。そこにあるものは agent に
	// ロードされない。ファイルは署名検証と inspect 成功の後にのみプラグインになる。
	// Use a unique temp file so concurrent uploads of the same plugin name
	// (or a stale .tmp left by a crash) cannot collide. The upload stays in
	// the temp file until inspect + meta + modules.json all succeed, so a
	// failure never leaves an orphan, potentially-runnable .so behind.
	// A-3: the upload lands in the quarantine directory (0700, inside
	// pluginsDir so the final rename is same-filesystem and atomic). Nothing
	// in there is loaded by the agent: the file only becomes a plugin after
	// signature verification and a successful inspect.
	quarantineDir, err := p.ensureQuarantineDir()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "検疫ディレクトリ作成失敗: "+err.Error())
		return
	}
	out, err := os.CreateTemp(quarantineDir, name+".so.tmp-*")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "一時ファイル作成失敗: "+err.Error())
		return
	}
	tmpPath := out.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	written, err := io.Copy(out, file)
	if cerr := out.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "ファイル書き込み失敗: "+err.Error())
		return
	}

	if p.logger != nil {
		kelog.LogInfo(p.logger, "api.plugin_upload_recv", name, written, r.RemoteAddr)
	}

	// A-3: plugin-inspect の前に Ed25519 署名を検証する。
	// .so を検査するとそれをロードし（init() を実行する）、
	// plugins.require_signature=true では未認証ファイルがその段階に達しては
	// ならない。
	//
	// 署名ソースの順序:
	//   1. .so 内の埋め込みトレーラ  (KIZUNA_SIG_V1; 1 ファイル、優先)
	//   2. multipart フォームの分離 .sig パート（legacy、対応）
	// 署名が全く無いファイルはサードパーティ候補だ。
	//
	// Origin ポリシー:
	//   - 署名あり + 検証成功 -> "official"（Kizuna シリーズプラグイン）
	//   - 署名ありだが不正     -> 拒否（改ざん。決して許可しない）
	//   - 署名なし             -> "third_party"。運用者がリスクを確認した
	//     場合のみ許可（confirm_unsigned=true）。
	// A-3: verify the Ed25519 signature BEFORE plugin-inspect.
	// Inspecting a .so loads it (running its init()), so with
	// plugins.require_signature=true an unauthenticated file must never reach
	// that step.
	//
	// Signature source order:
	//   1. embedded trailer in the .so  (KIZUNA_SIG_V1; one file, preferred)
	//   2. detached .sig part in the multipart form (legacy, supported)
	// A file with no signature at all is a third-party candidate.
	//
	// Origin policy:
	//   - signature present + verifies -> "official" (a Kizuna-series plugin)
	//   - signature present but bad    -> reject (tampered; never allowed)
	//   - signature absent             -> "third_party", allowed only after
	//     the operator confirms the risk (confirm_unsigned=true).
	sigBytes, err := readUploadedSignature(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, rerr := os.ReadFile(tmpPath)
	if rerr != nil {
		writeJSONError(w, http.StatusInternalServerError, "検疫ファイルの読み込みに失敗: "+rerr.Error())
		return
	}
	// 公開鍵が設定されていれば、plugins.require_signature が false でも署名を検証する。
	//
	// require_signature は未署名アップロードが許容されるかを決めるもので、
	// 「存在する」署名の検証まで無効化してはならない。以前はブロック全体が
	// `if WantSignature()` で包まれていたため、既定（false）ではすべての
	// アップロードが third_party と記録された。本当に署名済みの Kizuna プラグインが
	// 赤い「サードパーティ」監査アラートを受け取り、アラート履歴で untrusted と
	// マークされ、同梱の Web UI がサンドボックス iframe 経由で配信されていた。
	// Origin はファイルの性質であり、ポリシーの性質ではない。
	// Verify a signature whenever a public key is configured, even when
	// plugins.require_signature is false.
	//
	// require_signature decides whether an UNSIGNED upload is acceptable; it
	// must not also disable verification of a signature that IS present.
	// Previously the whole block was wrapped in `if WantSignature()`, so with
	// the default (false) every upload was recorded as third_party — a
	// genuinely signed Kizuna plugin got the red "third-party" audit alert,
	// was marked untrusted in the alert history, and its bundled web UI was
	// served through the sandboxed iframe. Origin is a property of the file,
	// not of the policy.
	origin := "third_party"
	sigSource := ""
	pub, keyErr := p.loadPublicKey()
	if keyErr != nil {
		// 設定済みだが使用不能な鍵はハードエラーだ。ポリシーは起動時に検証される
		// ため、これは鍵ファイルが変わったか消えたことを意味する。すべての
		// アップロードを黙って未署名扱い（全プラグインを third_party へ
		// ダウングレードする）にするのではなく拒否する。
		// A configured-but-unusable key is a hard error: the policy is
		// validated at startup, so this means the key file changed or
		// disappeared. Refuse rather than silently treat every upload as
		// unsigned (which would downgrade every plugin to third_party).
		writeJSONError(w, http.StatusInternalServerError, "公開鍵の読み込みに失敗: "+keyErr.Error())
		return
	}
	if pub != nil {
		// 1) 埋め込みトレーラ（優先）。embOK=false はトレーラが全く無いことを
		//    意味し、その場合下の分離 .sig を試す。
		// 1) embedded trailer (preferred). embOK=false means there is no
		//    trailer at all; then we try the detached .sig below.
		embOK, embErr := pluginsig.VerifyEmbedded(data, pub)
		switch {
		case embOK && embErr != nil:
			if p.logger != nil {
				kelog.LogError(p.logger, "api.plugin_sig_failed", name, embErr)
			}
			p.reportAudit("plugin_upload_rejected",
				"埋め込み署名の検証に失敗したため、プラグイン「"+name+"」の追加を拒否しました。追加元: "+remoteIP(r.RemoteAddr),
				"Rejected plugin \""+name+"\": embedded signature verification failed. From: "+remoteIP(r.RemoteAddr))
			writeJSONError(w, http.StatusBadRequest, "プラグイン署名の検証に失敗しました: "+embErr.Error())
			return
		case embOK:
			origin = "official"
			sigSource = "embedded"
		case len(sigBytes) > 0:
			// 2) 分離 .sig（legacy 経路）。
			// 2) detached .sig (legacy path).
			if verr := pluginsig.VerifyBytes(data, sigBytes, pub, name+".so"); verr != nil {
				if p.logger != nil {
					kelog.LogError(p.logger, "api.plugin_sig_failed", name, verr)
				}
				p.reportAudit("plugin_upload_rejected",
					"署名検証に失敗したため、プラグイン「"+name+"」の追加を拒否しました。追加元: "+remoteIP(r.RemoteAddr),
					"Rejected plugin \""+name+"\": signature verification failed. From: "+remoteIP(r.RemoteAddr))
				writeJSONError(w, http.StatusBadRequest, "プラグイン署名の検証に失敗しました: "+verr.Error())
				return
			}
			origin = "official"
			sigSource = "detached"
		default:
			// 3) 署名が全く無い -> サードパーティ候補。ポリシーが署名を要求する
			//    場合、運用者がリスクを明示的に確認しなければならず、記録された
			//    origin がその承認だ。要求しない場合、アップロードは third_party
			//    として受け入れられる（agent は meta.json が承認を記録しない限り
			//    依然ロードを拒否する）。
			// 3) no signature at all -> third-party candidate. When the policy
			//    requires a signature, the operator must explicitly confirm the
			//    risk; the recorded origin is that approval. When it does not,
			//    the upload is accepted as third_party (the agent still refuses
			//    to load it unless meta.json records the approval).
			if p.policy.WantSignature() && !isUnsignedConfirmed(r) {
				writeJSON(w, http.StatusConflict, map[string]interface{}{
					"error":              "このプラグインは署名がありません。純正か確認が取れませんでした。",
					"needs_confirmation": true,
					"origin":             "third_party",
					"name":               name,
				})
				return
			}
			if p.logger != nil {
				kelog.LogWarn(p.logger, "api.plugin_sig_missing", name)
			}
		}
		if sigSource != "" && p.logger != nil {
			kelog.LogInfo(p.logger, "api.plugin_sig_ok", name+" ("+sigSource+")")
		}
	}

	// 一時ファイルを所定の位置へ移す「前に」検査する。検査できないプラグインは
	// インストールしてはならない。plugin-inspect はメタデータを読むために .so を
	// ロードするため、検査失敗はそのファイルがロード可能な Go プラグインでは
	// ない（誤ったアーキ、破損、非プラグイン）ことを意味する。コミットすると
	// 壊れた/悪意ある .so をディスクへ置き、agent が次の再起動でロードしてしまう。
	// アップロードを拒否し、deferred クリーンアップに一時ファイルを除去させる。
	// Inspect the temp file BEFORE moving it into place. A plugin that cannot
	// be inspected must NOT be installed: plugin-inspect loads the .so to read
	// its metadata, so a failed inspect means the file is not a loadable Go
	// plugin (wrong arch, corrupt, or a non-plugin). Committing it would place
	// a broken/malicious .so on disk for the agent to load on the next restart.
	// Reject the upload and let the deferred cleanup remove the temp file.
	meta, err := p.inspectPlugin(r.Context(), name, tmpPath, r.RemoteAddr)
	if err != nil {
		if p.logger != nil {
			kelog.LogError(p.logger, "api.plugin_verify_failed", name, err)
		}
		writeJSONError(w, http.StatusBadRequest,
			"プラグインの検証に失敗したため登録できません（.so が壊れているか、対応していない形式です）: "+err.Error())
		return
	}

	// 正規名はプラグイン自身の Name() と一致しなければならない。agent は
	// plugin.Name() でモジュールレジストリをキー付けし、modules.json と meta.json は
	// この名前でキー付けされる。もし異なれば、agent は一方の名前で登録するのに
	// dashboard は他方で参照し、セキュリティイベントとバックアップ結果が黙って
	// 落とされる。プラグイン自己申告の名前（サニタイズ済み）を優先し、使用不能な
	// 場合のみアップロード名へフォールバックする。
	// The canonical name MUST match the plugin's own Name(): the agent keys
	// its module registry by plugin.Name(), while modules.json and meta.json
	// are keyed by this name. If they differ, the agent registers the plugin
	// under one name while the dashboard looks it up under the other, so
	// security events and backup results are silently dropped. Prefer the
	// plugin's self-declared name (sanitized); fall back to the upload name
	// only when it is unusable.
	canonical := name
	if meta != nil && meta.Name != "" {
		safe, serr := sanitizeName(meta.Name)
		if serr != nil {
			// プラグインが識別子に使えないモジュール名（空白やパス区切りを
			// 含む）を報告している。agent はその名前で登録するのに
			// modules.json はアップロード名を使うため、そのイベント/結果は
			// 落とされる。壊れた登録を生むより、大声で拒否する。
			// The plugin reports a module name that cannot be used as an
			// identifier (e.g. it contains spaces or a path separator). The
			// agent would register it under that name while modules.json
			// uses the upload name, so its events/results would be dropped.
			// Reject it loudly rather than create a broken registration.
			writeJSONError(w, http.StatusBadRequest,
				"プラグインの Name() が不正です（英数字・ハイフン・アンダースコアのみ使用可）: "+meta.Name)
			return
		}
		canonical = safe
	}

	// Web UI は「アップロード名」で展開された（<name>.web）が、.so / meta.json /
	// modules.json は正規名でキー付けされる。プラグイン自身の Name() が
	// アップロードファイル名と異なる場合、配信パス /plugins/<canonical>/ と
	// HasWebUI チェック（どちらも meta.Name 由来）は存在しないディレクトリを
	// 見に行き、カードの iframe が 404 になって Web UI が欠落して見える。
	// ディレクトリを正規名へリネームする。ここでの失敗は致命ではなく
	// （カードが描画されないだけ）、inspectPlugin の許容と一致する。
	// The web UI was extracted under the UPLOAD name (<name>.web), but the
	// .so / meta.json / modules.json are keyed by the canonical name. When the
	// plugin's own Name() differs from the uploaded file name, the served
	// path /plugins/<canonical>/ and the HasWebUI check (both derived from
	// meta.Name) would look at a directory that does not exist, so the card's
	// iframe 404s and the web UI appears missing. Rename the directory to the
	// canonical name; a failure here is non-fatal (the card just will not
	// render), matching inspectPlugin's own tolerance.
	if canonical != name {
		p.reconcileWebUIDir(name, canonical)
	}

	// インストール済みプラグインのファイルに触れる「前に」重複を拒否する。
	// 2 つの確認が必要だ:
	//   1. modules.json がまだそれを列挙している（通常ケース）、かつ
	//   2. modules.json が無くても .so / .meta.json がディスクに存在する。
	// 確認 2 が無いと、modules.json からモジュールを削除する（別操作）だけで、
	// 同名の後日のアップロードが storage.Get テストを通過し、下の os.Rename で
	// インストール済み .so を上書きし、動作中のプラグインを破壊できてしまう。
	// Refuse a duplicate BEFORE touching the installed plugin's files.
	// Two checks are needed:
	//   1. modules.json still lists it (normal case), and
	//   2. the .so / .meta.json exist on disk even if modules.json does not.
	// Without check 2, deleting a module from modules.json (a separate
	// operation) would let a later upload with the same name pass the
	// storage.Get test and overwrite the installed .so via os.Rename below,
	// destroying a working plugin.
	if p.storage.Get(canonical) != nil {
		writeJSONError(w, http.StatusConflict,
			"同じプラグインが既に登録されています。先に既存のプラグインを削除してから、もう一度お試しください: "+canonical)
		return
	}
	meta.Name = canonical
	meta.SOFilename = canonical + ".so"
	meta.Origin = origin
	// カードには、運用者のラベルがプラグイン自身の名前と異なるときだけそれを表示する。
	// そうでなければプラグイン由来のきれいな display_name の方が良いタイトルだ。
	// Show the operator's label on the card only when it differs from the
	// plugin's own name; otherwise the pretty display_name (from the plugin)
	// is the better title.
	if label != "" && label != canonical {
		meta.UserLabel = label
	}
	soPath = filepath.Join(p.pluginsDir, canonical+".so")
	metaPath = filepath.Join(p.pluginsDir, canonical+".meta.json")
	if fileExists(soPath) || fileExists(metaPath) {
		writeJSONError(w, http.StatusConflict,
			"同じプラグインのファイルが既に存在します。先に既存のプラグインを削除してから、もう一度お試しください: "+canonical)
		return
	}

	// JSON 失敗がコミット済み .so を残さないよう、modules.json エントリを先に組み立てる。
	// Build the modules.json entry up front so a JSON failure cannot leave a
	// committed .so either.
	cfgBytes, err := json.Marshal(map[string]string{"plugin_path": soPath})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "設定JSON生成失敗: "+err.Error())
		return
	}
	modConfig := ModuleConfig{
		Name:    canonical,
		Type:    "plugin",
		Enabled: true,
		Config:  json.RawMessage(cfgBytes),
	}

	// 検査済み .so を所定の位置へ移し、その後 meta.json を書く。meta.json を
	// 書けなければ .so をロールバックし、孤立を残さない。
	// Move the inspected .so into place, then write meta.json. If meta.json
	// cannot be written, roll back the .so so no orphan remains.
	if err := os.Rename(tmpPath, soPath); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "リネーム失敗: "+err.Error())
		return
	}
	committed = true

	// A-3: 分離署名をインストール済み .so の隣に保つ。agent は plugin.Open の前に
	// <plugin>.so.sig を検証するため、署名済みアップロードはロード可能なままで、
	// 後で require_signature を有効にしても全プラグインの再アップロードを強いない。
	//
	// A-4: os.CreateTemp() は .so を 0600 で作り、アップロード者は dashboard ユーザー、
	// agent は kizuna-eye で動くため、インストール済みファイルは world-readable に
	// しなければならない。そうしないと署名チェックが fail-closed になり、移行後に
	// 全プラグインが黙ってロードを止める。他者は読み取り専用で、所有者（dashboard
	// ユーザー）だけが書き込み・置換・削除できる。
	// A-3: keep the detached signature next to the installed .so. The agent
	// verifies <plugin>.so.sig before plugin.Open, so a signed upload stays
	// loadable, and turning on require_signature later does not force every
	// plugin to be re-uploaded.
	//
	// A-4: os.CreateTemp() produced the .so as 0600 and the uploader is the
	// dashboard user while the agent runs as kizuna-eye, so the installed
	// files must be made world-readable. Otherwise the signature check fails
	// closed and every plugin silently stops loading after the migration.
	// Read-only for others: only the owner (the dashboard user) can write,
	// replace or delete them.
	sigPath := pluginsig.SigPath(soPath)
	if err := os.Chmod(soPath, 0o644); err != nil {
		_ = os.Remove(soPath)
		writeJSONError(w, http.StatusInternalServerError, "配置した .so のパーミッション設定失敗: "+err.Error())
		return
	}
	if len(sigBytes) > 0 {
		// fsutil.WriteFileAtomic: pluginsDir は agent ユーザーが書き込めるため、
		// 事前に <name>.so.sig をシンボリックリンクに差し替えておくと
		// os.WriteFile が任意ファイルへ書き込んでしまう。atomic write は
		// 最終要素の symlink を拒否し、fsync してから rename する。
		// fsutil.WriteFileAtomic: pluginsDir is writable by the agent user, so
		// a pre-planted symlink at <name>.so.sig would make os.WriteFile
		// write into an arbitrary file. The atomic write refuses a symlink at
		// the final component and fsyncs before rename.
		if err := fsutil.WriteFileAtomic(sigPath, sigBytes, 0o644); err != nil {
			_ = os.Remove(soPath)
			writeJSONError(w, http.StatusInternalServerError, "署名ファイルの書き込み失敗: "+err.Error())
			return
		}
	}

	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	// A-4: AGENT (kizuna-eye) はこの meta.json を読み、未署名プラグインが
	// サードパーティとして承認されたかを判定する（isThirdPartyApproved）。
	// dashboard はログインユーザーとしてこれを書くため、0600 だと agent に
	// 読めず、承認済みサードパーティプラグインがロード時に拒否される。0644 は
	// インストール済み .so と一致し、両者が読める。fsutil.WriteFileAtomic は
	// symlink ターゲットを拒否し、rename 前に fsync する。
	// A-4: the AGENT (kizuna-eye) reads this meta.json to decide whether an
	// unsigned plugin was approved as third-party (isThirdPartyApproved).
	// The dashboard writes it as the login user, so 0600 would make it
	// unreadable for the agent and a confirmed third-party plugin would be
	// refused on load. 0644 matches the installed .so and keeps the file
	// readable by both. fsutil.WriteFileAtomic rejects a symlink target and
	// fsyncs before rename.
	if err := fsutil.WriteFileAtomic(metaPath, metaBytes, 0o644); err != nil {
		_ = os.Remove(soPath)
		_ = os.Remove(sigPath)
		writeJSONError(w, http.StatusInternalServerError, "meta.json 書き込み失敗: "+err.Error())
		return
	}

	// modules.json へ登録する。Add は内部で永続化し、保存失敗時にメモリ内リストを
	// ロールバックする。失敗時は .so と meta.json も除去し、プラグインが
	// 半端に登録されたまま残らないようにする。
	// Register in modules.json. Add persists internally and rolls back its
	// in-memory list on save failure; on failure we also remove the .so and
	// meta.json so the plugin is not left half-registered.
	if err := p.storage.Add(modConfig); err != nil {
		if p.logger != nil {
			kelog.LogWarn(p.logger, "api.module_register_failed", name, err)
		}
		_ = os.Remove(soPath)
		_ = os.Remove(sigPath)
		_ = os.Remove(metaPath)
		writeJSONError(w, http.StatusInternalServerError, "モジュール登録に失敗しました: "+err.Error())
		return
	}

	// 監査: プラグインのインストールはコード実行境界であり、ログだけでなく
	// アラート履歴に記録しなければならない。運用者のラベル（カードタイトルと
	// 一致）を表示し、origin を言葉で明示する。
	// Audit: installing a plugin is a code-execution boundary and must be
	// recorded in the alert history, not only logged. Show the operator's
	// label (matching the card title) and spell the origin out in words.
	displayName := canonical
	if meta.UserLabel != "" {
		displayName = meta.UserLabel
	}
	// 純正かサードパーティーかで前置きを切り替える。
	// Switch the prefix by official vs third-party.
	prefix := "純正プラグイン"
	prefixEn := "Official plugin"
	if origin == "third_party" {
		prefix = "サードパーティー製プラグイン"
		prefixEn = "Third-party plugin"
	}
	p.reportAudit("plugin_upload",
		prefix+"「"+displayName+"」が追加されました。追加元: "+remoteIP(r.RemoteAddr),
		prefixEn+" \""+displayName+"\" was added. From: "+remoteIP(r.RemoteAddr))

	writeJSON(w, http.StatusOK, meta)
}

// handleList は登録済みプラグインを返す。
// handleList returns the registered plugins.
func (p *PluginManager) handleList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(p.pluginsDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "plugins ディレクトリ読み込み失敗: "+err.Error())
		return
	}

	list := make([]*PluginMeta, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		metaPath := filepath.Join(p.pluginsDir, e.Name())
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta PluginMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		// 空の名前と重複をスキップする。異なる meta ファイルが同じプラグイン名を
		// 指し得る（例: はぐれ "Name.meta.json"）。このガードが無いとプラグインが
		// 一覧に 2 回現れる。
		// Skip empty names and duplicates. Different meta files can point to the
		// same plugin name (e.g. a stray "Name.meta.json"); without this guard
		// the plugin would appear twice in the list.
		if meta.Name == "" || seen[meta.Name] {
			continue
		}
		seen[meta.Name] = true
		// Web UI ディレクトリが存在するかフラグを立てる。
		// Flag whether a web UI directory exists.
		webDir := filepath.Join(p.pluginsDir, meta.Name+".web")
		if st, err := os.Stat(webDir); err == nil && st.IsDir() {
			meta.HasWebUI = true
		}
		list = append(list, &meta)
	}

	// サーバーの絶対パス (pluginsDir) は応答に含めない。UI は使わず、
	// 開示すると operator 以上にファイルシステム配置を漏らすだけだ。
	// Do not expose the server's absolute path (pluginsDir): the UI does not
	// use it and disclosing it leaks the filesystem layout to operators.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"plugins": list,
	})
}

// handleDelete はプラグインを削除する。
// handleDelete deletes a plugin.
func (p *PluginManager) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "プラグイン名が指定されていません")
		return
	}
	if _, err := sanitizeName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 削除前に meta.json を読み、監査メッセージが運用者のラベル（内部名と
	// 異なり得る）を使えるようにする。
	// Read meta.json before removing it so the audit message can use the
	// operator's label (which may differ from the internal name).
	deleteLabel := name
	if data, rerr := os.ReadFile(filepath.Join(p.pluginsDir, name+".meta.json")); rerr == nil {
		var m PluginMeta
		if json.Unmarshal(data, &m) == nil && m.UserLabel != "" {
			deleteLabel = m.UserLabel
		}
	}

	// Delete は内部で永続化し、保存失敗時にロールバックする。保存失敗
	// （ディスク満杯、権限）を 404 と報告してはならない。
	// Delete persists internally and rolls back on save failure. A save
	// failure (disk full, permissions) must not be reported as 404.
	if err := p.storage.Delete(name); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrModuleNotFound) {
			status = http.StatusNotFound
		}
		writeJSONError(w, status, err.Error())
		return
	}

	// 最初に .so を除去する。それが実行可能な成果物だからだ。除去できなければ
	// エラーを報告し、呼び出し側が実行可能な孤立がディスクに残り得ることを
	// 知るようにする（成功を偽らない）。
	// Remove the .so first: it is the runnable artifact. If it cannot be
	// removed, report an error so the caller knows a runnable orphan may
	// remain on disk, instead of falsely claiming success.
	if err := os.Remove(filepath.Join(p.pluginsDir, name+".so")); err != nil && !os.IsNotExist(err) {
		if p.logger != nil {
			kelog.LogError(p.logger, "api.plugin_so_delete_failed", name, err)
		}
		writeJSONError(w, http.StatusInternalServerError, ".so の削除に失敗しました: "+err.Error())
		return
	}
	_ = os.Remove(filepath.Join(p.pluginsDir, name+".meta.json"))
	// A-3: 分離署名も落とし、同名で別の .so を後日アップロードしてもこの
	// プラグインの署名を継承できないようにする。
	// A-3: drop the detached signature too, so a later upload of a different
	// .so under the same name cannot inherit this plugin's signature.
	_ = os.Remove(pluginsig.SigPath(filepath.Join(p.pluginsDir, name+".so")))
	// プラグイン同梱の Web UI ディレクトリがあれば除去し、削除後に古いアセットが
	// 残らないようにする。
	// Remove the plugin's bundled web UI directory, if any, so no stale
	// assets remain after deletion.
	_ = os.RemoveAll(filepath.Join(p.pluginsDir, name+".web"))

	if p.logger != nil {
		kelog.LogInfo(p.logger, "api.plugin_module_deleted", name)
	}
	// 監査: プラグインの削除はコード実行境界の変更だ。
	// Audit: removing a plugin is a code-execution boundary change.
	p.reportAudit("plugin_delete",
		"プラグイン「"+deleteLabel+"」が削除されました。削除元: "+remoteIP(r.RemoteAddr),
		"Plugin \""+deleteLabel+"\" was deleted. From: "+remoteIP(r.RemoteAddr))

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// handleRenameLabel は登録済みプラグインの表示専用ラベルを、plugins/<name>.meta.json を
// 書き換えて更新する。プラグイン自身の名前、その .so、modules.json は触らないため、
// イベントとバックアップは動作し続ける。空の user_label は上書きをクリアし、
// カードは display_name へフォールバックする。
// handleRenameLabel updates the display-only label of a registered plugin by
// rewriting plugins/<name>.meta.json. The plugin's own name, its .so and
// modules.json are untouched, so events and backups keep working. An empty
// user_label clears the override and the card falls back to display_name.
func (p *PluginManager) handleRenameLabel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "プラグイン名が指定されていません")
		return
	}
	if _, err := sanitizeName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	var body struct {
		UserLabel string `json:"user_label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "リクエストの解析に失敗しました: "+err.Error())
		return
	}
	label, err := sanitizeLabel(body.UserLabel)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	metaPath := filepath.Join(p.pluginsDir, name+".meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "プラグインのメタ情報が見つかりません: "+err.Error())
		return
	}
	var meta PluginMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "メタ情報の読み込みに失敗しました: "+err.Error())
		return
	}
	if label == "" || label == meta.Name {
		meta.UserLabel = ""
	} else {
		meta.UserLabel = label
	}
	out, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "メタ情報の生成に失敗しました: "+err.Error())
		return
	}
	// fsutil.WriteFileAtomic: シンボリックリンクを拒否し、一時ファイルに
	// mode を適用して fsync してから rename する（L-12: ここだけ手書きの
	// temp+rename が残っていた）。A-4: agent がこの meta.json を読んで
	// サードパーティ承認を判定するため、0644（.so と同じ）にする。
	// fsutil.WriteFileAtomic: reject a symlink, apply the mode to a temp
	// file, fsync, then rename (L-12: this was the last hand-rolled
	// temp+rename left). A-4: the agent reads this meta.json to decide
	// third-party approval, so 0644 (same as the .so).
	if err := fsutil.WriteFileAtomic(metaPath, out, 0o644); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "メタ情報の保存に失敗しました: "+err.Error())
		return
	}
	if p.logger != nil {
		kelog.LogInfo(p.logger, "api.plugin_label_renamed", name, label)
	}
	writeJSON(w, http.StatusOK, meta)
}

// inspectPlugin は plugin-inspect を実行し、結果をパースする。
// inspectPlugin runs plugin-inspect and parses the result.
func (p *PluginManager) inspectPlugin(
	parent context.Context, name, soPath, remoteAddr string,
) (*PluginMeta, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	// モジュールの Web UI（同梱していれば）を plugins/<name>.web/ へ展開する。
	// ディレクトリは .so の隣にあり、/plugins/<name>/ で配信される。作成失敗が
	// インストールを妨げてはならない（カードが描画されないだけ）。inspect は
	// このディレクトリ下の検証済みパスにのみ書く。
	// Extract the module's web UI (if it ships one) into plugins/<name>.web/.
	// The directory lives next to the .so and is served at /plugins/<name>/.
	// A failure to create it must not block the install (the card just will
	// not render); inspect writes only validated paths under this dir.
	webOut := filepath.Join(p.pluginsDir, name+".web")

	cmd, err := p.inspectCommand(ctx, soPath, webOut)
	if err != nil {
		return nil, err
	}
	out, err := cmd.Output()
	if err != nil {
		// plugin-inspect は構造化された失敗を STDOUT に JSON で報告し、非ゼロで
		// 終了する（きれいな "could not load" ケースでは stderr に何も出さない）。
		// stderr だけを読むと空メッセージ（"plugin-inspect 失敗 (exit=1): "）を
		// 生み、実際の理由を運用者から隠していた。JSON の error_message、次に
		// stderr、最後に終了コードを優先する。
		// plugin-inspect reports a structured failure as JSON on STDOUT and
		// exits non-zero (it also prints nothing on stderr for a clean
		// "could not load" case). Reading only stderr produced an empty
		// message ("plugin-inspect 失敗 (exit=1): "), which hid the real
		// reason from the operator. Prefer the JSON error_message, then
		// stderr, then the exit code.
		reason := inspectFailureReason(out, err)
		return nil, fmt.Errorf("plugin-inspect 失敗: %s", reason)
	}

	var raw struct {
		Success        bool                   `json:"success"`
		Name           string                 `json:"name"`
		DisplayName    string                 `json:"display_name"`
		Tag            string                 `json:"tag"`
		Description    string                 `json:"description"`
		IntervalSec    float64                `json:"interval_sec"`
		Fields         []module.ConfigField   `json:"fields"`
		IsBackup       bool                   `json:"is_backup"`
		DashboardCards []module.DashboardCard `json:"dashboard_cards"`
		WebUIPath      string                 `json:"web_ui_path"`
		ErrorMessage   string                 `json:"error_message"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("plugin-inspect の出力パース失敗: %w", err)
	}
	if !raw.Success {
		return nil, fmt.Errorf("plugin-inspect がエラーを返しました: %s", raw.ErrorMessage)
	}

	if raw.Name == "" {
		raw.Name = name
	} else if _, err := sanitizeName(raw.Name); err != nil {
		// プラグインがファイルシステム安全でない名前（例: パス区切りを含む）を
		// 自己申告した。サニタイズ済みアップロード名へフォールバックし、
		// meta.json がパストラバーサル名を運べないようにする。
		// The plugin self-reported a name that is not filesystem-safe
		// (e.g. contains a path separator). Fall back to the sanitized
		// upload name so meta.json cannot carry a path-traversal name.
		raw.Name = name
	}

	return &PluginMeta{
		Name:           raw.Name,
		DisplayName:    raw.DisplayName,
		Tag:            raw.Tag,
		SOFilename:     name + ".so",
		Description:    raw.Description,
		IntervalSec:    raw.IntervalSec,
		Fields:         raw.Fields,
		IsBackup:       raw.IsBackup,
		DashboardCards: raw.DashboardCards,
		WebUIPath:      raw.WebUIPath,
		UploadedAt:     time.Now().Format(time.RFC3339),
		UploaderIP:     remoteAddr,
	}, nil
}

// inspectFailureReason は失敗した plugin-inspect 実行から人間が読める原因を抽出する。
// まず stdout の JSON error_message（通常経路）、次に stderr、最後に汎用の
// 終了コードメッセージを優先する。
// inspectFailureReason extracts the human-readable cause from a failed
// plugin-inspect run. It prefers the JSON error_message on stdout (the normal
// path), then stderr, then a generic exit-code message.
func inspectFailureReason(stdout []byte, runErr error) string {
	if len(stdout) > 0 {
		var r struct {
			ErrorMessage string `json:"error_message"`
		}
		if json.Unmarshal(stdout, &r) == nil && strings.TrimSpace(r.ErrorMessage) != "" {
			return r.ErrorMessage
		}
	}
	if ee, ok := runErr.(*exec.ExitError); ok {
		if s := strings.TrimSpace(string(ee.Stderr)); s != "" {
			return fmt.Sprintf("exit=%d: %s", ee.ExitCode(), s)
		}
		return fmt.Sprintf("exit=%d（詳細不明）", ee.ExitCode())
	}
	return runErr.Error()
}

// inspectCommand は検疫済み .so 用の plugin-inspect 呼び出しを組み立てる。
//
// 隔離が有効（既定）な場合、検査は bubblewrap 内で実行する。すべての namespace
// （network、pid、ipc、uts、user、cgroup）を unshare し、ファイルシステムは
// 読み取り専用でバインドし、/tmp は新しい tmpfs だ。プラグインのロードはその
// init() を実行するため、検査中の敵対的 .so はネットワーク到達も書き込み可能パスも
// 得ない。bwrap 欠落は（無保護実行への無言のダウングレードではなく）ハードエラー
// （fail-closed）だ。
// inspectCommand builds the plugin-inspect invocation for a quarantined .so.
//
// With isolation enabled (the default) inspection runs inside bubblewrap: all
// namespaces are unshared (network, pid, ipc, uts, user, cgroup), the
// filesystem is bound read-only and /tmp is a fresh tmpfs. Loading a plugin
// executes its init(), so a hostile .so being inspected gets no network reach
// and no writable path. A missing bwrap is a hard error (fail-closed) rather
// than a silent downgrade to unprotected execution.
func (p *PluginManager) inspectCommand(ctx context.Context, soPath, webOut string) (*exec.Cmd, error) {
	base := []string{"--so", soPath}
	if webOut != "" {
		base = append(base, "--web-out", webOut)
	}
	if !p.policy.WantInspectIsolation() {
		return exec.CommandContext(ctx, p.inspectBin, base...), nil
	}
	bin := p.bwrapBin
	if bin == "" {
		bin = p.policy.BwrapBinary()
	}
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("プラグイン検査の隔離に必要な bwrap が見つかりません (%s)。bubblewrap を導入するか、dashboard_config.json の plugins.inspect_isolation を \"off\" にしてください: %w", bin, err)
	}
	args := []string{
		"--unshare-all",
		"--die-with-parent",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
	}
	// web-out ディレクトリはサンドボックス内で書き込み可能でなければならない。
	// 自身へ rw バインドする（pluginsDir 下にあり、dashboard が所有する）。
	// The web-out directory must be writable inside the sandbox: bind it
	// rw over itself (it is under pluginsDir, which the dashboard owns).
	if webOut != "" {
		if err := os.MkdirAll(webOut, 0o755); err == nil {
			args = append(args, "--bind", webOut, webOut)
		}
	}
	args = append(args, "--", p.inspectBin)
	args = append(args, base...)
	return exec.CommandContext(ctx, bin, args...), nil
}

// readUploadedSignature はアップロードと共に送られた任意の分離署名を返す。
// 既存のアップロードフォームが動き続けるよう 2 形式を受け付ける:
//
//	signature      - .so の隣で選択された .sig ファイル
//	signature_text - base64 または hex で貼り付けられた署名
//
// 署名が全く無い場合は (nil, nil) を返す。それが許容されるかは呼び出し側
// （plugins.require_signature）が決める。
// readUploadedSignature returns the optional detached signature sent with an
// upload. Two forms are accepted so the existing upload form keeps working:
//
//	signature      - a .sig file selected next to the .so
//	signature_text - the signature pasted as base64 or hex
//
// No signature at all returns (nil, nil); whether that is acceptable is
// decided by the caller (plugins.require_signature).
func readUploadedSignature(r *http.Request) ([]byte, error) {
	f, _, err := r.FormFile("signature")
	if err == nil {
		defer f.Close()
		data, rerr := io.ReadAll(io.LimitReader(f, maxSignatureBytes+1))
		if rerr != nil {
			return nil, fmt.Errorf("署名ファイルの読み込みに失敗: %w", rerr)
		}
		if len(data) > maxSignatureBytes {
			return nil, fmt.Errorf("署名ファイルが大きすぎます（最大 %d バイト）", maxSignatureBytes)
		}
		return data, nil
	}
	if !errors.Is(err, http.ErrMissingFile) {
		return nil, fmt.Errorf("署名ファイルの解析に失敗: %w", err)
	}
	if v := strings.TrimSpace(r.FormValue("signature_text")); v != "" {
		return []byte(v), nil
	}
	return nil, nil
}

// isUnsignedConfirmed はクライアントが検証可能な署名無しのプラグインの
// インストールを明示的に承認したかを返す。値は multipart フォームから取り
// （"true"/"1"/"yes"）、アップロードに署名が無い場合にのみ意味を持つ。
// isUnsignedConfirmed reports whether the client explicitly acknowledged
// installing a plugin without a verifiable signature. The value is taken from
// the multipart form ("true"/"1"/"yes") and is only meaningful when the
// upload has no signature.
func isUnsignedConfirmed(r *http.Request) bool {
	v := strings.ToLower(strings.TrimSpace(r.FormValue("confirm_unsigned")))
	return v == "true" || v == "1" || v == "yes"
}
