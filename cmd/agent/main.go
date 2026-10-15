package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"plugin"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"Kizuna-Eye/internal/api"
	"Kizuna-Eye/internal/pluginsig"
	"Kizuna-Eye/internal/system"
	"Kizuna-Eye/internal/updater"
	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/module"
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		return &bytes.Buffer{}
	},
}

// checkDependencies は agent が依存する外部ツールが導入されているかを確認する。
// 不足ツールは警告（致命ではない）だ。rsync はバックアッププラグイン、
// smartctl は S.M.A.R.T ディスク健康状態収集が必要とする。警告はログファイルと
// stderr の両方へ出し、対話起動でもサービス管理経由でも見えるようにする。
// checkDependencies verifies that external tools the agent relies on are
// installed. Missing tools are warned (not fatal): rsync is needed by backup
// plugins, smartctl by the S.M.A.R.T disk health collection. The warning goes
// to both the log file and stderr so it is visible when started interactively
// or via a service manager.
func checkDependencies(lg *logger.Logger) {
	// cmd -> 用途。
	// cmd -> what it is used for.
	deps := []struct {
		cmd string
		use string
	}{
		{"rsync", "バックアッププラグイン"},
		{"smartctl", "ディスクS.M.A.R.T（温度・健康状態・書込量）"},
	}

	var missing []string
	for _, d := range deps {
		if _, err := exec.LookPath(d.cmd); err != nil {
			missing = append(missing, d.cmd)
			msg := fmt.Sprintf("必要なコマンドが見つかりません: %s（%s で使用）", d.cmd, d.use)
			if lg != nil {
				lg.WarnT("agent.dep_missing", d.cmd, d.use)
			}
			fmt.Fprintln(os.Stderr, "[AGENT] 警告: "+msg)
		}
	}

	if len(missing) > 0 {
		hint := "install.sh を実行するか、お使いのパッケージマネージャで導入してください"
		if lg != nil {
			lg.WarnT("agent.dep_missing_list", missing, hint)
		}
		fmt.Fprintf(os.Stderr, "[AGENT] 警告: 不足ツール: %v — %s\n", missing, hint)
	}
}

var loadedPlugins = struct {
	sync.Mutex
	// names はプラグインが実際に登録された名前（自身の Name()）でキー付けされ、
	// manager.Get/Unregister が使うものだ。
	// names is keyed by the name a plugin actually registered under (its own
	// Name()), which is what manager.Get/Unregister use.
	names map[string]bool
	// byCfg は modules.json のエントリ名を、プラグインが登録された名前へ対応付ける。
	// これにより、名前がプラグインの Name() と異なる手編集の modules.json でも
	// 正しく無効化/再設定できる。
	// byCfg maps a modules.json entry name to the name the plugin registered
	// under, so a hand-edited modules.json whose name differs from the
	// plugin's Name() can still be disabled/reconfigured correctly.
	byCfg map[string]string
	// cache は .so パスを、plugin.Open が発見したコンストラクタへ対応付ける。
	// Go は 1 プロセスで同じパスを 2 回 Open できないが、それが返すコンストラクタ
	// （NewPluginModule）は何度でも呼べる。キャッシュすると、無効化→有効化の
	// サイクルが agent を再起動せずに「新しい」インスタンスを構築できる。エントリは
	// ファイルの mtime/size も記録する。ディスク上の .so が置き換わるとキャッシュ
	// 済みコンストラクタは古いコードを指すため無効化され、再 Open が試みられる
	// （Go は拒否し、agent の再起動が必要になる — 唯一再起動が要るケース）。
	// cache maps a .so path to the constructor discovered by plugin.Open.
	// Go cannot Open the same path twice in one process, but the constructor
	// it returns (NewPluginModule) can be called any number of times. Caching
	// it lets a disable→enable cycle build a FRESH instance without restarting
	// the agent. The entry also records the file's mtime/size: if the .so on
	// disk is replaced, the cached constructor points at the old code, so it
	// is invalidated and a re-Open is attempted (which Go refuses, requiring
	// an agent restart — the only remaining case that needs one).
	cache map[string]*pluginCacheEntry
}{
	names: make(map[string]bool),
	byCfg: make(map[string]string),
	cache: make(map[string]*pluginCacheEntry),
}

// pluginCacheEntry はキャッシュ済みのプラグインコンストラクタ 1 つ分だ
// （loadedPlugins.cache 参照）。
// pluginCacheEntry is one cached plugin constructor (see loadedPlugins.cache).
type pluginCacheEntry struct {
	ctor  func(module.Logger) module.PluginModule
	mtime time.Time
	size  int64
}

// moduleRuntime は全モジュールの最後のロード試行の結果を、その modules.json
// エントリ名でキー付けして保持する。dashboard はこれをモジュールカードに表示し、
// ロードに失敗したプラグインが黙って "active"（実際は違う）と表示されないように
// する。agent は実際のロード結果を観測できる唯一のコンポーネントなので、既に
// status と security イベントを運ぶ WebSocket でマップを公開する。
// moduleRuntime holds the outcome of the last load attempt for every module,
// keyed by its modules.json entry name. The dashboard shows this on the
// module card so a plugin that failed to load is not silently displayed as
// "active" (it is not). The agent is the only component that can observe the
// actual load result, so it publishes the map over the WebSocket that already
// carries status and security events.
var moduleRuntime = struct {
	sync.Mutex
	states   map[string]moduleRuntimeState
	lastSent string
}{
	states: make(map[string]moduleRuntimeState),
}

type moduleRuntimeState struct {
	// Status は "loaded"、"failed"、"disabled" のいずれかだ。
	// Status is one of "loaded", "failed", or "disabled".
	Status string `json:"status"`
	// ReasonCode は失敗原因の安定した識別子だ。フロントエンドが翻訳するため、
	// 運用者に表示されるメッセージは agent 自身の言語に固定されず、UI 言語に追随する。
	// ReasonCode is a stable identifier for the failure cause. The frontend
	// translates it, so the message shown to the operator follows the UI
	// language instead of being stuck in the agent's own language.
	ReasonCode string `json:"reason_code,omitempty"`
	// Reason は Status が "failed" のときの日本語の人間可読な原因だ。
	// 古い dashboard と agent 自身のログが使うフォールバックだ。
	// Reason is a Japanese human-readable cause when Status is "failed".
	// It is the fallback used by older dashboards and the agent's own logs.
	Reason string `json:"reason,omitempty"`
}

// wsWriter は WebSocket 接続への書き込みを直列化する。
// gorilla/websocket は接続ごとに 1 つの並行書き手しか許さないが、agent は
// status ループ、ping ループ、バックアップ結果ゴルーチンから書く。
// wsWriter serializes writes to a WebSocket connection.
// gorilla/websocket allows only one concurrent writer per connection,
// but the agent writes from the status loop, the ping loop, and
// backup-result goroutines.
type wsWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *wsWriter) WriteMessage(messageType int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.conn.WriteMessage(messageType, data)
}

func (w *wsWriter) WriteControl(messageType int, data []byte, deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteControl(messageType, data, deadline)
}

func main() {
	configPath := flag.String("config", "agent_config.json", "設定ファイルパス")
	modulesPath := flag.String("modules", "modules.json", "モジュール設定ファイルパス")
	flag.Parse()

	cfg, err := config.LoadAgentConfig(*configPath)
	if err != nil {
		log.Fatalf("設定読み込み失敗: %v", err)
	}

	// 既定は INFO とし、agent_config.json の log_level（"debug"）で明示的に
	// 有効化しない限り DEBUG ノイズを出さない。
	// Default to INFO so DEBUG noise is not emitted unless explicitly
	// enabled via agent_config.json's log_level ("debug").
	level := logger.INFO
	if cfg.LogLevel != "" {
		level = logger.ParseLevel(cfg.LogLevel)
	}
	lg := logger.NewLogger(&logger.Options{
		LogFile:        cfg.LogFile,
		Level:          level,
		Prefix:         "[AGENT]",
		UseUTC:         false,
		ForwardURL:     cfg.LogForward.URL,
		ForwardTimeout: cfg.LogForward.Timeout(),
	})
	defer lg.Sync()

	lg.InfoT("agent.start", cfg.Interval, cfg.DashboardURL)

	// 起動時に外部ツールの有無を確認する（致命的ではない）。
	// Check for external tools at startup (not fatal).
	checkDependencies(lg)

	ctx, cancel := context.WithCancel(context.Background())

	// ---- 自動更新（GitHub Releases を監視し、新しければ safe_update.sh）----
	// セキュリティ注意: 自動更新は GitHub リポジトリ/アカウントが侵害されると、
	// サーバーが悪性コードを自動でビルド・実行する経路（実質 RCE）になる。
	// 既定は無効。信頼できるリポジトリでのみ有効化すること。
	// ---- Auto-update (watch GitHub Releases, run safe_update.sh if newer) ----
	// Security note: auto-update becomes a path for the server to build and
	// run malicious code automatically (effectively RCE) if the GitHub repo
	// or account is compromised. Off by default; enable only for a fully
	// trusted repository.
	if au := cfg.AutoUpdate; au.Enabled {
		if lg != nil {
			lg.WarnT("agent.autoupdate_on", au.RepositoryURL)
		}
		script := au.UpdateScript
		if script == "" {
			script = "safe_update.sh"
		}
		interval := time.Duration(au.IntervalHours) * time.Hour
		if au.IntervalHours <= 0 {
			interval = 6 * time.Hour
		}
		if up := updater.New(updater.Config{
			Enabled:        true,
			RepositoryURL:  au.RepositoryURL,
			ArchiveName:    au.ArchiveName,
			UpdateScript:   script,
			Interval:       interval,
			CurrentVersion: api.Version,
		}, lg); up != nil {
			go up.Run(ctx.Done())
		}
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	manager := module.NewModuleManager(lg)

	// ---- System module ----
	// SystemConfig.Interval は整数秒だ。サブ秒値（例: 0.5）を int へ切り捨てると
	// 0 になり、黙って 1 秒へフォールバックするため、ここで最小 1 にクランプし、
	// 実効値をログする。
	// SystemConfig.Interval is in whole seconds. Truncating a sub-second
	// value (e.g. 0.5) to int would yield 0 and silently fall back to 1s,
	// so clamp to a minimum of 1 here and log the effective value.
	sysInterval := int(cfg.Interval)
	if sysInterval < 1 {
		sysInterval = 1
	}
	sysConfig := &system.SystemConfig{
		DiskPath:          cfg.DiskPath,
		Interval:          sysInterval,
		HealthInterval:    0,
		StaleThreshold:    0,
		ShutdownTimeout:   5,
		CPUErrorThreshold: 3,
	}
	sysModule := system.NewSystemModule(lg)
	if err := sysModule.Configure(sysConfig); err != nil {
		lg.ErrorT("agent.sys_module_cfg_failed", err)
	}
	if err := manager.Register(ctx, sysModule); err != nil {
		lg.ErrorT("agent.sys_module_reg_failed", err)
	}
	lg.InfoT("agent.sys_modules", sysInterval)

	// ---- Load plugins ----
	// 設定されたプラグインディレクトリ内の .so のみを許可するため、改ざんされた
	// modules.json が任意のライブラリをロードすることはできない。
	// Only .so files inside the configured plugins directory are allowed,
	// so a tampered modules.json cannot load an arbitrary library.
	pluginsDir := cfg.ResolvePluginsDir()
	lg.InfoT("agent.plugins_dir", pluginsDir)
	// A-3: 起動時にプラグイン認証ポリシーをログし、運用者が未署名 .so が依然
	// 受け入れられるかを即座に見られるようにする。
	// A-3: log the plugin authentication policy at startup, so an operator can
	// see immediately whether unsigned .so files are still accepted.
	if cfg.Plugins.WantSignature() {
		lg.InfoT("agent.sig_verify_enabled", cfg.Plugins.PublicKeyFile)
	} else {
		lg.WarnT("agent.sig_verify_disabled")
	}
	if err := loadPluginsFromConfig(ctx, *modulesPath, manager, lg, pluginsDir, cfg.Plugins); err != nil {
		lg.ErrorT("agent.plugin_load_error", err)
	}

	manager.Start(ctx)

	go watchModulesFile(ctx, *modulesPath, manager, lg, pluginsDir, cfg.Plugins)

	go func() {
		<-sigCh
		lg.InfoT("agent.shutdown_signal")
		cancel()
	}()

	for {
		select {
		case <-ctx.Done():
			lg.InfoT("agent.exit")
			manager.Stop()
			return
		default:
		}
		if err := runAgent(ctx, cfg, lg, manager); err != nil {
			lg.ErrorT("agent.run_error", err)
			time.Sleep(5 * time.Second)
		}
	}
}

// resolveAndCheckPluginPath は pluginPath を解決し、allowedDir 内でなければ
// 拒否する。これにより、改ざんされた modules.json（またはバグ）がファイルシステム
// のどこからでも任意の .so をロードするのを止める。両パスは実（symlink 無し）の
// 位置へ解決し、ディレクトリ内の symlink でそれを脱出できないようにする。
// resolveAndCheckPluginPath resolves pluginPath and rejects it unless it is
// inside allowedDir. This stops a tampered modules.json (or a bug) from
// loading an arbitrary .so from anywhere on the filesystem. Both paths are
// resolved to their real (symlink-free) location so a symlink inside the
// directory cannot be used to escape it.
func resolveAndCheckPluginPath(pluginPath, allowedDir string) (string, error) {
	if pluginPath == "" {
		return "", fmt.Errorf("plugin_path が空です")
	}
	absPlugin, err := filepath.Abs(pluginPath)
	if err != nil {
		return "", fmt.Errorf("plugin_path の解決に失敗: %w", err)
	}
	absDir, err := filepath.Abs(allowedDir)
	if err != nil {
		return "", fmt.Errorf("plugins ディレクトリの解決に失敗: %w", err)
	}

	// 実パス（symlink 解決）を優先し、plugins ディレクトリ外を指す symlink を
	// 拒否する。ファイルがまだ存在しない場合は abs パスへフォールバックする
	// （plugin.Open が明確なエラーで失敗する）。
	// Prefer real paths (resolve symlinks) so a symlink in the plugins dir
	// pointing outside it is rejected. Fall back to the abs path when the
	// file does not exist yet (plugin.Open will then fail with a clear error).
	realPlugin, perr := filepath.EvalSymlinks(absPlugin)
	if perr != nil {
		realPlugin = absPlugin
	}
	realDir, derr := filepath.EvalSymlinks(absDir)
	if derr != nil {
		realDir = absDir
	}

	rel, err := filepath.Rel(realDir, realPlugin)
	if err != nil {
		return "", fmt.Errorf("plugin_path が plugins ディレクトリ外です: %s", pluginPath)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("plugin_path が plugins ディレクトリ外です: %s", pluginPath)
	}
	if !strings.HasSuffix(realPlugin, ".so") {
		return "", fmt.Errorf("plugin_path は .so である必要があります: %s", pluginPath)
	}
	return realPlugin, nil
}

// loadPluginsFromConfig は modules.json を読み込んでプラグインを登録する
// loadPluginsFromConfig reads modules.json and registers the plugins.
func loadPluginsFromConfig(ctx context.Context, path string, manager module.ModuleManager, lg *logger.Logger, pluginsDir string, policy config.PluginSecurityConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// modules.json が無いのは正常（モジュール未登録）。初回起動のログを
			// 埋めないよう Debug に落とす。
			// A missing modules.json is normal (no modules registered). Drop
			// it to Debug so the first-start log is not spammed.
			lg.DebugT("agent.modules_missing", path)
			return nil
		}
		return fmt.Errorf("modules.json 読み込み失敗: %w", err)
	}

	var configs []struct {
		Name    string                 `json:"name"`
		Type    string                 `json:"type"`
		Enabled bool                   `json:"enabled"`
		Config  map[string]interface{} `json:"config"`
	}

	if err := json.Unmarshal(data, &configs); err != nil {
		return fmt.Errorf("modules.json パース失敗: %w", err)
	}

	loadedPlugins.Lock()
	defer loadedPlugins.Unlock()

	// modules.json にまだ存在する（有効・無効を問わず）全プラグイン名を追跡し、
	// ファイルから削除されたプラグインを下で停止できるようにする。
	// Track every plugin name still present in modules.json (enabled or not),
	// so a plugin that was deleted from the file can be stopped below.
	present := make(map[string]bool, len(configs))

	// このロードパスのランタイム status を、modules.json エントリ名でキー付けする。
	// dashboard へ公開し、ロードに失敗したプラグインがそのカードで "active" と
	// 表示されないようにする。
	// Runtime status for this load pass, keyed by the modules.json entry
	// name. Published to the dashboard so a plugin that failed to load is
	// not shown as "active" on its card.
	runtime := make(map[string]moduleRuntimeState, len(configs))

	for _, cfg := range configs {
		if cfg.Type != "plugin" {
			continue
		}
		present[cfg.Name] = true
		if !cfg.Enabled {
			// 無効化されたプラグインが以前ロード済みなら停止し、agent 再起動なしで
			// 実行を止める。modules.json の名前とプラグイン自身の Name() が異なる
			// 場合に備え、実際に登録された名前を解決する。
			// Stop a previously-loaded plugin when it is disabled, so it
			// stops executing without an agent restart. Resolve the name it
			// was actually registered under, in case modules.json's name and
			// the plugin's own Name() differ.
			tracked := cfg.Name
			if !loadedPlugins.names[tracked] {
				if reg, ok := loadedPlugins.byCfg[cfg.Name]; ok {
					tracked = reg
				} else if mod, ok := manager.Get(cfg.Name); ok {
					// Name() はプラグインのコードだ。ここで panic すると
					// （modules.json 再読み込み経路）、agent が落ちてはならない。
					// 空の結果は tracked == cfg.Name を保ち、それが安全な
					// フォールバックだ。
					// Name() is plugin code; a panic here (modules.json reload
					// path) must not crash the agent. An empty result leaves
					// tracked == cfg.Name, which is the safe fallback.
					if n := safeModuleName(mod, lg); n != "" {
						tracked = n
					}
				}
			}
			if loadedPlugins.names[tracked] {
				if err := manager.Unregister(tracked); err != nil {
					lg.ErrorT("agent.plugin_stop_failed", tracked, err)
				} else {
					delete(loadedPlugins.names, tracked)
					delete(loadedPlugins.byCfg, cfg.Name)
					lg.InfoT("agent.plugin_disabled", tracked)
				}
			} else {
				lg.InfoT("agent.plugin_skip", cfg.Name)
			}
			runtime[cfg.Name] = moduleRuntimeState{Status: "disabled"}
			continue
		}
		// このプラグインが実際に登録された名前（自身の Name()）を解決する。
		// modules.json のエントリ名と異なり得る。
		// Resolve the name this plugin actually registered under (its own
		// Name()), which may differ from the modules.json entry name.
		regName := cfg.Name
		if reg, ok := loadedPlugins.byCfg[cfg.Name]; ok {
			regName = reg
		}
		if loadedPlugins.names[regName] {
			// 既にロード済み: 実行中プラグインへ新しい設定を適用し、
			// interval_sec のような変更を再起動なしで反映する。
			// Already loaded: apply the new config to the running plugin
			// so changes like interval_sec take effect without a restart.
			if existing, ok := manager.Get(regName); ok {
				if configurable, ok := existing.(interface {
					Configure(config interface{}) error
				}); ok {
					if err := configurePlugin(configurable, cfg.Config); err != nil {
						lg.ErrorT("agent.plugin_reconfig_failed", regName, err)
					} else {
						manager.NotifyConfigChanged(regName)
						lg.InfoT("agent.plugin_config", regName)
					}
				}
			}
			runtime[cfg.Name] = moduleRuntimeState{Status: "loaded"}
			continue
		}

		pluginPath, ok := cfg.Config["plugin_path"].(string)
		if !ok || pluginPath == "" {
			lg.ErrorT("agent.plugin_path_invalid", cfg.Name)
			runtime[cfg.Name] = moduleRuntimeState{Status: "failed", ReasonCode: "invalid_plugin_path", Reason: "plugin_path が不正です"}
			continue
		}

		safePath, err := resolveAndCheckPluginPath(pluginPath, pluginsDir)
		if err != nil {
			lg.ErrorT("agent.plugin_rejected", cfg.Name, err)
			code, msg := classifyLoadError(err)
			runtime[cfg.Name] = moduleRuntimeState{Status: "failed", ReasonCode: code, Reason: msg}
			continue
		}

		registeredName, err := loadAndRegisterPlugin(ctx, safePath, cfg.Config, manager, lg, policy)
		if err != nil {
			lg.ErrorT("agent.plugin_load_failed", cfg.Name, err)
			code, msg := classifyLoadError(err)
			runtime[cfg.Name] = moduleRuntimeState{Status: "failed", ReasonCode: code, Reason: msg}
			continue
		}
		if registeredName == "" {
			registeredName = cfg.Name
		}
		// モジュールマネージャはプラグイン自身の Name() でプラグインをキー付けし、
		// modules.json は cfg.Name でキー付けされる。両者が異なる場合
		// （手編集の modules.json）、cfg.Name を追跡すると manager.Get(cfg.Name) が
		// 失敗し、そのプラグインのセキュリティイベント/バックアップ status が
		// 黙って落ちる。実際に登録された名前を追跡し、下の除去スイープがそれを
		// 止めないよう両名を "present" として記憶する。
		// The module manager keys plugins by the plugin's own Name(), while
		// modules.json is keyed by cfg.Name. If they differ (hand-edited
		// modules.json), tracking cfg.Name would make manager.Get(cfg.Name)
		// fail and silently drop the plugin's security events / backup
		// status. Track the name it actually registered under, and remember
		// both names as "present" so the removal sweep below does not stop it.
		if registeredName != cfg.Name {
			lg.WarnT("agent.plugin_name_mismatch", cfg.Name, registeredName, registeredName)
			present[registeredName] = true
			loadedPlugins.byCfg[cfg.Name] = registeredName
		}
		loadedPlugins.names[registeredName] = true
		lg.InfoT("agent.plugin_registered", registeredName)
		runtime[cfg.Name] = moduleRuntimeState{Status: "loaded"}
	}

	// ランタイムビューを公開する。status ループは変化時にそれを送るため、
	// ロードに失敗したモジュールが dashboard カードに見える。
	// Publish the runtime view. The status loop sends it on change, so a
	// module that failed to load is visible on the dashboard card.
	moduleRuntime.Lock()
	moduleRuntime.states = runtime
	moduleRuntime.Unlock()

	// 依然ロード済みだが modules.json に存在しなくなった（例: dashboard から
	// 削除された）プラグインを停止する。これが無いと、削除されたプラグインは
	// agent が再起動するまで動き続ける。disabled 分岐は enabled=false でまだ
	// 存在するエントリしか扱わないためだ。
	// Stop plugins that are still loaded but no longer present in modules.json
	// (e.g. deleted from the dashboard). Without this a removed plugin keeps
	// running until the agent is restarted, because the disabled branch only
	// handles an entry that still exists with enabled=false.
	for name := range loadedPlugins.names {
		if present[name] {
			continue
		}
		if err := manager.Unregister(name); err != nil {
			lg.ErrorT("agent.plugin_stop_failed", name, err)
			continue
		}
		delete(loadedPlugins.names, name)
		for cfgName, reg := range loadedPlugins.byCfg {
			if reg == name {
				delete(loadedPlugins.byCfg, cfgName)
			}
		}
		lg.InfoT("agent.plugin_removed", name)
	}

	return nil
}

// verifyPluginSignature はプラグイン .so の A-3 署名ポリシーを強制する。
// これは plugin.Open の「前に」実行しなければならない。Go プラグインを開くと
// その init() とパッケージレベル初期化子が実行されるため、未認証の .so は決して
// 開かれてはならない。
// verifyPluginSignature enforces the A-3 signature policy for a plugin .so.
// It MUST run before plugin.Open: opening a Go plugin executes its init() and
// package-level initializers, so an unauthenticated .so must never be opened.
func verifyPluginSignature(soPath string, policy config.PluginSecurityConfig, lg *logger.Logger) error {
	if !policy.WantSignature() {
		return nil
	}
	if strings.TrimSpace(policy.PublicKeyFile) == "" {
		return fmt.Errorf("plugins.require_signature=true ですが plugins.public_key_file が未設定です")
	}
	pub, err := pluginsig.LoadPublicKey(policy.PublicKeyFile)
	if err != nil {
		return fmt.Errorf("プラグイン公開鍵を読み込めません: %w", err)
	}
	// 埋め込みトレーラを優先し、後方互換のため分離 .sig へフォールバックする。
	// どちらの経路も plugin.Open の前に .so を認証する。署名が一切無いファイルのみ
	// 拒否される。
	// Prefer the embedded trailer, fall back to the detached .sig for
	// backward compatibility. Either path authenticates the .so before
	// plugin.Open; only a file with no signature at all is refused.
	source, err := pluginsig.VerifyFileAny(soPath, pub)
	if err != nil {
		if errors.Is(err, pluginsig.ErrNoSignature) {
			// 署名が一切ない。ダッシュボードで運用者が「純正と確認でき
			// ない」ことを承知のうえで追加したサードパーティー製だけを
			// 許容する（meta.json の origin=third_party が同意の記録）。
			// それ以外の未署名 .so は、たとえファイルが置かれていても
			// 拒否する。
			// No signature at all. Allow only third-party plugins that the
			// operator added via the dashboard knowing they cannot be
			// verified as official (meta.json's origin=third_party is the
			// record of consent). Any other unsigned .so is refused even if
			// the file was placed on disk.
			if isThirdPartyApproved(soPath) {
				if lg != nil {
					lg.WarnT("agent.plugin_third_party_allowed", soPath)
				}
				return nil
			}
			return fmt.Errorf("未署名のプラグインを拒否しました（埋め込み署名も %s もありません。plugin-sign --embed で署名してください）: %w", pluginsig.SigPath(soPath), err)
		}
		return fmt.Errorf("プラグイン署名の検証に失敗しました（改ざんまたは鍵不一致の可能性）: %w", err)
	}
	if lg != nil {
		lg.InfoT("agent.plugin_sig_ok", soPath+" ("+source+")")
	}
	return nil
}

// classifyLoadError はプラグインロードエラーの安定したコードと日本語の
// フォールバックメッセージを返す。コードは dashboard が UI 言語へ翻訳するもので、
// メッセージは古い dashboard と agent 自身のログが使うフォールバックだ。未知の
// エラーはコード "unknown" を使い、生のテキスト（冗長な plugin.Open 接頭辞を
// 剥がして長さを制限）をメッセージにする。
// classifyLoadError returns a stable code and a Japanese fallback message for
// a plugin load error. The code is what the dashboard translates to the UI
// language; the message is the fallback used by older dashboards and the
// agent's own logs. Unknown errors use code "unknown" with the raw text
// (redundant plugin.Open prefix stripped and length capped) as the message.
func classifyLoadError(err error) (code, message string) {
	if err == nil {
		return "", ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "plugin already loaded"),
		strings.Contains(msg, "既に一度ロードされています"):
		// plugin.Open は 1 プロセスで同じパスの 2 回目の Open を拒否する。
		// 通常はコンストラクタキャッシュがこれを避けるが、ディスク上の .so が
		// 置き換わったときには依然起こる。キャッシュ済みコンストラクタは古い
		// コードを指すため、agent は再 Open を試み、Go が拒否する。これが
		// 真に agent 再起動を要する唯一のケースなので、メッセージは一般的な
		// 失敗に見えるのではなくそう言わなければならない。
		// plugin.Open refuses a second Open of the same path in one process.
		// Normally the constructor cache avoids this, but it still happens
		// when the .so was replaced on disk: the cached constructor points at
		// the old code, so the agent tries a re-Open and Go refuses. That is
		// the one case that genuinely needs an agent restart, so the message
		// must say so instead of looking like a generic failure.
		return "already_loaded", "このモジュールは既に読み込まれています。.so を差し替えた場合は Agent の再起動が必要です。"
	case strings.Contains(msg, "no such file or directory"):
		return "no_such_file", ".so ファイルが見つかりません。パスを確認してください。"
	case strings.Contains(msg, "different version of package"):
		return "version_mismatch", "本体とビルドバージョンが一致しません。モジュールを再ビルドしてください。"
	case strings.Contains(msg, "未署名のプラグインを拒否"):
		return "unsigned_rejected", "署名がなく、承認記録もないため拒否しました。"
	case strings.Contains(msg, "署名の検証に失敗"):
		return "signature_mismatch", "署名の検証に失敗しました（改ざん、または鍵不一致の可能性）。"
	case strings.Contains(msg, "NewPluginModule が見つかりません"):
		return "missing_entrypoint", "プラグインの形式が不正です（NewPluginModule がありません）。"
	case strings.Contains(msg, "NewPluginModule のシグネチャが不正"):
		return "bad_entrypoint", "プラグインの形式が不正です（シグネチャ不一致）。"
	case strings.Contains(msg, "plugins ディレクトリ外"):
		return "outside_plugins_dir", ".so が plugins ディレクトリの外にあります。"
	case strings.Contains(msg, ".so である必要"):
		return "not_so", "対象ファイルが .so ではありません。"
	}
	if i := strings.LastIndex(msg, "): "); i >= 0 && i+3 < len(msg) {
		msg = msg[i+3:]
	}
	if runes := []rune(msg); len(runes) > 140 {
		msg = string(runes[:140]) + "…"
	}
	return "unknown", msg
}

// isThirdPartyApproved はプラグインの .meta.json が、運用者が dashboard 経由で
// サードパーティプラグインとして明示的に承認したことを記録しているかを返す。
// origin フィールドは、運用者が「未検証」オーバーレイを受け入れた後にのみ
// アップロード経路が書き込むため、その存在が同意の記録だ。meta ファイルが無い
// または読めない場合は「未承認」として扱い、手でディスクに置かれた未署名 .so は
// 依然拒否される。
// isThirdPartyApproved reports whether the plugin's .meta.json records that
// an operator explicitly confirmed it as a third-party plugin through the
// dashboard. The origin field is written by the upload path only after the
// operator accepted the "not verified" overlay, so its presence is the
// record of consent. A missing or unreadable meta file is treated as "not
// approved", so an unsigned .so dropped on disk by hand is still refused.
func isThirdPartyApproved(soPath string) bool {
	metaPath := strings.TrimSuffix(soPath, ".so") + ".meta.json"
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	var meta struct {
		Origin string `json:"origin"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	return meta.Origin == "third_party"
}

// loadAndRegisterPlugin はプラグインをロード・登録し、実際に登録された名前
// （プラグイン自身の Name()）を返す。modules.json のエントリ名と異なり得る。
// loadAndRegisterPlugin loads and registers the plugin and returns the name it
// was actually registered under (the plugin's own Name()), which may differ
// from the modules.json entry name.
func loadAndRegisterPlugin(
	ctx context.Context,
	path string,
	pluginCfg map[string]interface{},
	manager module.ModuleManager,
	lg *logger.Logger,
	policy config.PluginSecurityConfig,
) (string, error) {
	// 注意: 呼び出し側（loadPluginsFromConfig）は既に loadedPlugins のロックを
	// 保持しているため、この関数は再度取ってはならない（sync.Mutex は再入不可で、
	// そうすると agent 全体がデッドロックする）。
	// NOTE: the caller (loadPluginsFromConfig) already holds loadedPlugins'
	// lock, so this function must not take it again (sync.Mutex is not
	// reentrant; doing so deadlocks the whole agent).

	// コンストラクタを解決する。plugin.Open は 1 プロセス 1 パスにつき 1 回しか
	// 実行できないが、それが返すコンストラクタは繰り返し呼べるため、無効化→有効化の
	// サイクルはキャッシュ済みコンストラクタを再利用し、"already loaded" で失敗せずに
	// 新しいインスタンスを構築する。
	//
	// 変更された .so（異なる mtime/size）はキャッシュ済みコンストラクタを無効化する。
	// それは古いコードを指し、Go はパスを再び Open できないため、ロードは失敗し
	// 運用者は agent を再起動しなければならない。
	// Resolve the constructor. plugin.Open can run only once per path per
	// process, but the constructor it returns can be called repeatedly, so a
	// disable→enable cycle reuses the cached constructor to build a fresh
	// instance instead of failing with "already loaded".
	//
	// A changed .so (different mtime/size) invalidates the cached constructor:
	// it points at the old code, and Go cannot Open the path again, so the
	// load fails and the operator must restart the agent.
	var mtime time.Time
	var size int64
	if fi, statErr := os.Stat(path); statErr == nil {
		mtime = fi.ModTime()
		size = fi.Size()
	}

	var ctor func(module.Logger) module.PluginModule
	if e, ok := loadedPlugins.cache[path]; ok && e.mtime.Equal(mtime) && e.size == size {
		ctor = e.ctor
	} else {
		// A-3: このプロセスへマップされる前に .so を認証する。ここは
		// すべてのロード経路（初回ロードと modules.json 再読み込み）の
		// チョークポイントであり、dashboard のアップロードチェックが
		// 迂回されても、検証済み状態に達しなかったプラグインは実行できない。
		// A-3: authenticate the .so before it is mapped into this process.
		// This is the choke point for every load path (initial load and
		// modules.json reload), so a plugin that never reached a verified
		// state cannot execute even if the dashboard's upload checks were
		// bypassed.
		if err := verifyPluginSignature(path, policy, lg); err != nil {
			return "", err
		}
		p, err := plugin.Open(path)
		if err != nil {
			return "", fmt.Errorf("plugin.Open 失敗 (%s): %w", path, err)
		}
		sym, err := p.Lookup("NewPluginModule")
		if err != nil {
			return "", fmt.Errorf("NewPluginModule が見つかりません: %w", err)
		}
		newFunc, ok := sym.(func(module.Logger) module.PluginModule)
		if !ok {
			return "", fmt.Errorf("NewPluginModule のシグネチャが不正です")
		}
		ctor = newFunc
		loadedPlugins.cache[path] = &pluginCacheEntry{ctor: newFunc, mtime: mtime, size: size}
	}

	pluginMod, err := constructPlugin(ctor, lg)
	if err != nil {
		return "", err
	}

	if configurable, ok := pluginMod.(interface {
		Configure(config interface{}) error
	}); ok {
		if err := configurePlugin(configurable, pluginCfg); err != nil {
			return "", fmt.Errorf("プラグイン設定適用失敗: %w", err)
		}
	}

	if err := manager.Register(ctx, pluginMod); err != nil {
		return "", fmt.Errorf("モジュール登録失敗: %w", err)
	}

	// Name() はプラグインのコードだ。Register は既に panic 安全なラッパーで
	// 検証済みだが、これは 2 回目の呼び出しだ。2 回目の呼び出しで Name() が
	// panic する（または単に非決定的な）プラグインが、ここでも agent を
	// 落としてならない。空名へフォールバックし、呼び出し側がそれを cfg.Name に
	// 変える。
	// Name() is plugin code. Register already validated it via a panic-safe
	// wrapper, but this is a second call: a plugin whose Name() panics on the
	// second invocation (or is simply non-deterministic) must not crash the
	// agent here either. Fall back to an empty name, which the caller turns
	// into cfg.Name.
	return safePluginName(pluginMod, lg), nil
}

// safeModuleName は panic 回復付きで mod.Name() を返し、Name() で panic する
// プラグインが agent を落とせないようにする（例: 無効化されたモジュールが登録された
// 名前を解決するとき）。panic 時は空文字列を返す。
// safeModuleName returns mod.Name() with panic recovery, so a plugin that
// panics in Name() cannot crash the agent (e.g. while resolving the name a
// disabled module registered under). An empty string is returned on panic.
func safeModuleName(mod module.Module, lg *logger.Logger) (name string) {
	defer func() {
		if r := recover(); r != nil {
			if lg != nil {
				lg.ErrorT("agent.plugin_name_panic", r)
			}
			name = ""
		}
	}()
	return mod.Name()
}

// safePluginName は panic 回復付きで mod.Name() を返し、Name() で panic する
// プラグインが登録後にその名前を読むとき agent を落とせないようにする。
// safePluginName returns mod.Name() with panic recovery, so a plugin that
// panics in Name() cannot crash the agent when its name is read after
// registration.
func safePluginName(mod module.PluginModule, lg *logger.Logger) (name string) {
	defer func() {
		if r := recover(); r != nil {
			if lg != nil {
				lg.ErrorT("agent.plugin_name_panic", r)
			}
			name = ""
		}
	}()
	return mod.Name()
}

// constructPlugin は panic 回復付きでプラグインのコンストラクタを実行し、
// サードパーティプラグインの init コード内の panic が agent を落とさないようにする。
// constructPlugin runs the plugin's constructor with panic recovery, so a
// panic in third-party plugin init code does not crash the agent.
func constructPlugin(newFunc func(module.Logger) module.PluginModule, lg *logger.Logger) (mod module.PluginModule, err error) {
	defer func() {
		if r := recover(); r != nil {
			if lg != nil {
				lg.ErrorT("agent.plugin_ctor_panic", r)
			}
			mod = nil
			err = fmt.Errorf("plugin constructor panicked: %v", r)
		}
	}()
	mod = newFunc(lg)
	// nil を返すコンストラクタは、manager.Register が nil インターフェースで
	// module.Name() を呼び、agent を落とすため、ここで拒否する。
	// A constructor that returns nil would make manager.Register call
	// module.Name() on a nil interface and crash the agent; reject it here.
	if mod == nil {
		return nil, fmt.Errorf("plugin constructor returned nil")
	}
	return mod, nil
}

// configurePlugin は panic 回復付きでプラグイン設定を適用する。
// configurePlugin applies plugin config with panic recovery.
func configurePlugin(configurable interface{ Configure(interface{}) error }, config map[string]interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin Configure panicked: %v", r)
		}
	}()
	return configurable.Configure(config)
}

// watchModulesFile は modules.json を監視し、変更があれば再読み込みする。
// watchModulesFile watches modules.json for changes and reloads.
func watchModulesFile(ctx context.Context, path string, manager module.ModuleManager, lg *logger.Logger, pluginsDir string, policy config.PluginSecurityConfig) {
	// modules.json の変更検知は mtime だけに頼らない。mtime は
	// ファイルシステムの秒精度に丸められるため、同一秒内の書き換え
	// （保存 → すぐ別の保存）を取りこぼす。サイズと inode も併用し、
	// 「前回と違う状態になったか」で判定する。
	// Change detection for modules.json does not rely on mtime alone. mtime
	// is rounded to the filesystem's second precision, so writes within the
	// same second (save, then another save immediately) are missed. Size and
	// inode are used too, so the decision is "did the state differ from last
	// time?".
	type modFileState struct {
		modTime time.Time
		size    int64
		inode   uint64
	}
	readState := func(p string) (modFileState, bool) {
		var st modFileState
		info, err := os.Stat(p)
		if err != nil {
			return st, false
		}
		st.modTime = info.ModTime()
		st.size = info.Size()
		// fileInode はプラットフォーム別に分離されている。syscall.Stat_t は
		// Unix 系にのみ存在し、ここで無条件に参照すると Windows ビルド（そして
		// 開発機での `go test ./...`）が壊れた。非 Unix のフォールバックは 0 を
		// 返し、size+mtime を変更シグナルとして残す。
		// fileInode is platform-split: syscall.Stat_t exists only on
		// Unix-like systems, and referencing it here unconditionally broke
		// the Windows build (and therefore `go test ./...` on a developer
		// machine). The non-Unix fallback returns 0, leaving size+mtime as
		// the change signal.
		st.inode = fileInode(info)
		return st, true
	}

	var lastState modFileState
	if st, ok := readState(path); ok {
		lastState = st
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			st, err := readState(path)
			if !err {
				continue
			}
			if st == lastState {
				continue
			}

			lastState = st
			lg.InfoT("agent.modules_changed")

			if err := loadPluginsFromConfig(ctx, path, manager, lg, pluginsDir, policy); err != nil {
				lg.ErrorT("agent.plugin_reload_failed", err)
			}
		}
	}
}

// ============================================================
// runAgent: Phase 8-5 で読み取りループを追加
// runAgent: the read loop was added in Phase 8-5
// ============================================================
func runAgent(ctx context.Context, cfg *config.AgentConfig, lg *logger.Logger, manager module.ModuleManager) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	// 共有 agent トークンは X-Kizuna-Agent-Token ヘッダ「のみ」で送る。
	// dashboard はそこで受け取り、?token= クエリパラメータを意図的に無視する。
	// クエリ文字列はアクセスログ、Referer ヘッダ、ブラウザ履歴へ漏れるためだ（G-2）。
	// トークンが空のとき dashboard は従来のヒューリスティック検出へフォールバックする。
	// Send the shared agent token in the X-Kizuna-Agent-Token header ONLY.
	// The dashboard accepts it there and deliberately ignores a ?token=
	// query parameter, because a query string leaks into access logs,
	// Referer headers and browser history (G-2). When the token is empty,
	// the dashboard falls back to heuristic detection (legacy).
	dialURL := cfg.DashboardURL
	header := http.Header{}
	// 常に role=agent を宣言する。dashboard はこれが存在するときのみ接続を
	// agent 候補とみなし、ブラウザが agent 接続と誤認され（乗っ取られ）ないようにする。
	// Always declare role=agent. The dashboard only considers a connection
	// as a candidate agent when this is present, which stops a browser from
	// being mistaken for (and hijacking) the agent connection.
	sep := "?"
	if strings.Contains(dialURL, "?") {
		sep = "&"
	}
	dialURL = dialURL + sep + "role=agent"
	if cfg.Token != "" {
		header.Set("X-Kizuna-Agent-Token", cfg.Token)
	}

	conn, _, err := dialer.Dial(dialURL, header)
	if err != nil {
		return err
	}
	defer conn.Close()
	lg.InfoT("agent.ws_connected")

	// この接続へのすべての書き込みを直列化する。
	// Serialize all writes to this connection.
	writer := &wsWriter{conn: conn}

	// agent は dashboard から小さなコマンドメッセージしか受信しない。
	// The agent only receives small command messages from the dashboard.
	conn.SetReadLimit(1 << 20) // 1 MiB
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})

	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()

	// Ping送信ループ
	// Ping-send loop
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				if err := writer.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
					lg.DebugT("agent.ping_failed", err)
					return
				}
			}
		}
	}()

	// dashboard からのコマンド用の読み取りループ。
	// Read loop for commands from the dashboard.
	readCtx, readCancel := context.WithCancel(ctx)
	defer readCancel()

	go func() {
		for {
			select {
			case <-readCtx.Done():
				return
			default:
			}

			_, msg, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					lg.DebugT("agent.read_error", err)
				}
				return
			}
			conn.SetReadDeadline(time.Now().Add(120 * time.Second))

			// コマンド（action フィールドを持つ）かどうかを確認する。
			// Check whether it is a command (has an action field).
			var probe struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal(msg, &probe); err != nil || probe.Action == "" {
				continue
			}

			handleDashboardCommand(ctx, msg, manager, writer, lg)
		}
	}()

	interval := time.Duration(cfg.Interval * float64(time.Second))
	// サブナノ秒の interval が 0 へ丸められ、time.NewTicker が panic する
	// （"non-positive interval"）のを防ぐ。validateAgentConfig は interval <= 0 しか
	// 拒否しないため、ごく小さな正値は依然ここへ到達し得る。
	// Guard against a sub-nanosecond interval rounding to 0, which would make
	// time.NewTicker panic ("non-positive interval"). validateAgentConfig only
	// rejects interval <= 0, so a tiny positive value can still reach here.
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 初期モジュールランタイムビュー（とその status スナップショット）を送り、
	// dashboard カードが最初の描画から実際のロード結果を表示するようにする。
	// Send the initial module runtime view (and its status snapshot) so the
	// dashboard card shows the real load result from the first render.
	if err := sendModuleRuntime(writer, lg); err != nil {
		lg.DebugT("agent.module_status_failed", err)
	}

	lastSent, err := sendStatus(writer, manager, lg)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// セキュリティイベントは毎 tick フラッシュし、その配送遅延を status
			// 収集間隔に縛らないようにする。
			// Security events are flushed every tick so their delivery latency
			// is not tied to the status collection interval.
			sendSecurityEvents(writer, manager, lg)

			// モジュールのロード結果は modules.json が変わるかロードが失敗した
			// ときのみ変わる。関数自体が未変更の集合をスキップする。
			// Module load results change only when modules.json changes or a
			// load fails; the function itself skips unchanged sets.
			if err := sendModuleRuntime(writer, lg); err != nil {
				lg.DebugT("agent.module_status_failed", err)
			}

			// 送信間隔は収集間隔より短くなり得る（例: system モジュールが毎秒
			// サンプルするのに interval=0.2s）。未変更 status の再送はディープ
			// コピー、JSON エンコード、全クライアントへのブロードキャストを
			// 要するため、スキップする。
			// The send interval can be shorter than the collection interval
			// (e.g. interval=0.2s while the system module samples every 1s).
			// Re-sending an unchanged status costs a deep copy, a JSON encode
			// and a broadcast to every client, so skip it.
			if ts := statusTimestamp(manager); ts != 0 && ts == lastSent {
				continue
			}
			ts, err := sendStatus(writer, manager, lg)
			if err != nil {
				lg.ErrorT("agent.send_failed", err)
				return err
			}
			lastSent = ts
		}
	}
}

// ============================================================
// sendSecurityEvents は保留中のセキュリティイベントを Dashboard へ転送する。
// 各イベントは個別の {event:"security_alert"} メッセージとして送られ、dashboard が
// 個別に通知・記録できるようにする。
// sendSecurityEvents forwards pending security events to the Dashboard.
// Each event is sent as a separate {event:"security_alert"} message so the
// dashboard can notify and record it individually.
// ============================================================
func sendSecurityEvents(w *wsWriter, manager module.ModuleManager, lg *logger.Logger) {
	loadedPlugins.Lock()
	names := make([]string, 0, len(loadedPlugins.names))
	for name := range loadedPlugins.names {
		names = append(names, name)
	}
	loadedPlugins.Unlock()

	for _, name := range names {
		mod, ok := manager.Get(name)
		if !ok {
			continue
		}
		provider, ok := mod.(module.SecurityEventProvider)
		if !ok {
			continue
		}
		events := drainSecurityEventsSafe(provider, lg)
		for i, ev := range events {
			payload := map[string]interface{}{
				"event":     "security_alert",
				"plugin":    ev.Plugin,
				"category":  ev.Category,
				"level":     ev.Level,
				"title":     ev.Title,
				"message":   ev.Message,
				"source":    ev.Source,
				"actor":     ev.Actor,
				"ip":        ev.IP,
				"timestamp": ev.Timestamp.Format(time.RFC3339),
			}
			// 英語表示用 (タスク9)。空のときは送らない。
			// For English display (task 9). Not sent when empty.
			if ev.TitleEN != "" {
				payload["title_en"] = ev.TitleEN
			}
			if ev.MessageEN != "" {
				payload["message_en"] = ev.MessageEN
			}
			// 詳細表示用フィールド (タスク1)。空のときは送らない。
			// Detail-view fields (task 1). Not sent when empty.
			if ev.Command != "" {
				payload["command"] = ev.Command
			}
			if ev.DetectFile != "" {
				payload["detect_file"] = ev.DetectFile
			}
			if ev.DetectLine != 0 {
				payload["detect_line"] = ev.DetectLine
			}
			if ev.Remediation != "" {
				payload["remediation"] = ev.Remediation
			}
			if ev.RelatedLog != "" {
				payload["related_log"] = ev.RelatedLog
			}
			data, err := json.Marshal(payload)
			if err != nil {
				continue
			}
			if err := w.WriteMessage(websocket.TextMessage, data); err != nil {
				// イベントは既にプラグインから排出されている。未送信の残りを戻し、
				// 一時的な書き込みエラーがセキュリティイベント（SSH ブルート
				// フォース、FIM など）を黙って落とさないようにする。
				// The events were already drained from the plugin. Put the
				// unsent remainder back so a transient write error does not
				// silently drop security events (SSH brute force, FIM, ...).
				if requeuer, ok := provider.(module.SecurityEventRequeuer); ok {
					requeueSecurityEventsSafe(requeuer, events[i:], lg)
				}
				if lg != nil {
					lg.ErrorT("agent.sec_event_failed", err)
				}
				return
			}
			if lg != nil {
				lg.InfoT("agent.sec_event_sent", ev.Level, ev.Title, ev.Category)
			}
		}
	}
}

// requeueSecurityEventsSafe は panic 回復付きでプラグインの RequeueSecurityEvents を
// 呼ぶ。sendSecurityEvents は agent のメインゴルーチン（runAgent の送信ループ）で
// 動き、これはプラグインメソッドだ。ここで panic すると回復されず、下の
// DrainSecurityEvents と全く同様に agent 全体（および他の全プラグインとシステム監視）を
// 終了させる。イベントは既に排出済みなので panic 時は失われるが、1 バッチを失う方が
// agent を殺すよりはるかにましだ。
// requeueSecurityEventsSafe calls the plugin's RequeueSecurityEvents with
// panic recovery. sendSecurityEvents runs in the agent's main goroutine (the
// runAgent send loop), and this is a plugin method: a panic here is
// unrecovered and would terminate the whole agent (and every other plugin and
// the system monitoring), exactly like DrainSecurityEvents below. The events
// are already drained, so on panic they are lost, but losing one batch is far
// better than killing the agent.
func requeueSecurityEventsSafe(requeuer module.SecurityEventRequeuer, events []module.SecurityEvent, lg *logger.Logger) {
	defer func() {
		if r := recover(); r != nil {
			if lg != nil {
				lg.ErrorT("agent.sec_event_requeue_panic", r)
			}
		}
	}()
	requeuer.RequeueSecurityEvents(events)
}

// drainSecurityEventsSafe は panic 回復付きでプラグインの DrainSecurityEvents を
// 呼び、バグ持ちプラグインが agent を落とせないようにする。
// drainSecurityEventsSafe calls the plugin's DrainSecurityEvents with panic
// recovery, so a buggy plugin cannot crash the agent.
func drainSecurityEventsSafe(provider module.SecurityEventProvider, lg *logger.Logger) (events []module.SecurityEvent) {
	defer func() {
		if r := recover(); r != nil {
			if lg != nil {
				lg.ErrorT("agent.sec_event_panic", r)
			}
			events = nil
		}
	}()
	return provider.DrainSecurityEvents()
}

// ============================================================
// statusTimestamp は system モジュールの最新 status タイムスタンプをコピーせずに
// 返し、送信ループが未変更 status を安価に検出できるようにする。
// statusTimestamp returns the system module's latest status timestamp without
// copying it, so the send loop can cheaply detect an unchanged status.
// ============================================================
func statusTimestamp(manager module.ModuleManager) int64 {
	sysMod, ok := manager.Get("system")
	if !ok {
		return 0
	}
	sysModule, ok := sysMod.(*system.SystemModule)
	if !ok {
		return 0
	}
	return sysModule.StatusTimestamp()
}

// sendStatus はステータスを WebSocket で送信し、送信したステータスの
// タイムスタンプを返す（未送信なら 0）。
// sendStatus sends the status over the WebSocket and returns the timestamp of
// the status that was sent (0 if nothing was sent).
// ============================================================
func sendStatus(w *wsWriter, manager module.ModuleManager, lg *logger.Logger) (int64, error) {
	sysMod, ok := manager.Get("system")
	if !ok {
		return 0, nil
	}
	sysModule, ok := sysMod.(*system.SystemModule)
	if !ok {
		return 0, nil
	}

	s := sysModule.GetStatus()
	if s == nil {
		return 0, nil
	}

	if bs := collectBackupStatus(manager); bs != nil {
		s.Backup.Name = bs.Name
		s.Backup.LastRun = bs.LastRun
		s.Backup.NextRun = bs.NextRun
		s.Backup.Status = bs.Status
		s.Backup.Size = bs.Size
	}

	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)

	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return 0, err
	}

	if err := w.WriteMessage(websocket.TextMessage, buf.Bytes()); err != nil {
		return s.Timestamp, err
	}

	lg.DebugT("agent.status_sent")
	return s.Timestamp, nil
}

// sendModuleRuntime はモジュールのロード結果を "module_status" イベントとして
// dashboard へ送るが、前回送信以降に集合が変わったときのみだ。変更チェックが無いと
// 毎 tick 再送され、ブラウザが理由なくモジュール一覧を再描画する。比較はマップの
// 正規 JSON を使い、同じ内容に対して安定している。
// sendModuleRuntime sends the module load results to the dashboard as a
// "module_status" event, but only when the set changed since the last send.
// Without the change check it would be re-sent every tick and the browser
// would re-render the module list for no reason. The comparison uses the
// canonical JSON of the map, which is stable for the same content.
func sendModuleRuntime(w *wsWriter, lg *logger.Logger) error {
	moduleRuntime.Lock()
	states := make(map[string]moduleRuntimeState, len(moduleRuntime.states))
	for k, v := range moduleRuntime.states {
		states[k] = v
	}
	moduleRuntime.Unlock()

	data, err := json.Marshal(states)
	if err != nil {
		return err
	}

	moduleRuntime.Lock()
	same := string(data) == moduleRuntime.lastSent
	if !same {
		moduleRuntime.lastSent = string(data)
	}
	moduleRuntime.Unlock()
	if same {
		return nil
	}

	payload := map[string]interface{}{
		"event":    "module_status",
		"statuses": states,
	}
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return err
	}
	if err := w.WriteMessage(websocket.TextMessage, buf.Bytes()); err != nil {
		return err
	}
	if lg != nil {
		lg.DebugT("agent.module_status_sent")
	}
	return nil
}

// ============================================================
// collectBackupStatus（バックアップ状態の収集）
// collectBackupStatus (collect backup status)
// ============================================================
func collectBackupStatus(manager module.ModuleManager) *module.BackupStatus {
	loadedPlugins.Lock()
	names := make([]string, 0, len(loadedPlugins.names))
	for name := range loadedPlugins.names {
		names = append(names, name)
	}
	loadedPlugins.Unlock()

	if len(names) == 0 {
		return nil
	}

	var latest *module.BackupStatus
	var latestTime time.Time

	for _, name := range names {
		if name == "system" {
			continue
		}

		mod, ok := manager.Get(name)
		if !ok {
			continue
		}

		provider, ok := mod.(module.BackupStatusProvider)
		if !ok {
			continue
		}

		status := backupStatusSafe(provider)
		if status == nil {
			continue
		}

		if status.LastRun == "" {
			if latest == nil {
				latest = status
			}
			continue
		}

		t, err := time.Parse(time.RFC3339, status.LastRun)
		if err != nil {
			if latest == nil {
				latest = status
			}
			continue
		}

		if latest == nil || latest.LastRun == "" || t.After(latestTime) {
			latest = status
			latestTime = t
		}
	}

	return latest
}

// backupStatusSafe は panic 回復付きでプラグインの GetBackupStatus を呼ぶ。
// collectBackupStatus は agent の status 送信経路で動くため、ここでプラグインが
// panic すると agent プロセス全体が落ちる。
// backupStatusSafe calls a plugin's GetBackupStatus with panic recovery.
// collectBackupStatus runs on the agent's status-send path, so a plugin panic
// here would crash the whole agent process.
func backupStatusSafe(provider module.BackupStatusProvider) (st *module.BackupStatus) {
	defer func() {
		if r := recover(); r != nil {
			st = nil
		}
	}()
	return provider.GetBackupStatus()
}

// ============================================================
// Dashboard からのコマンド処理。
// Command handling from the dashboard.
// ============================================================

// handleDashboardCommand は Dashboard から受信したコマンドを処理する。
// handleDashboardCommand processes a command received from the Dashboard.
func handleDashboardCommand(
	ctx context.Context,
	raw []byte,
	manager module.ModuleManager,
	w *wsWriter,
	lg *logger.Logger,
) {
	var cmd struct {
		Action    string `json:"action"`
		Plugin    string `json:"plugin"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &cmd); err != nil {
		if lg != nil {
			lg.DebugT("agent.cmd_parse_failed", err)
		}
		return
	}

	if cmd.Action != "run_backup" {
		if lg != nil {
			lg.DebugT("agent.cmd_unknown", cmd.Action)
		}
		return
	}

	if lg != nil {
		lg.InfoT("agent.manual_recv", cmd.Plugin, cmd.RequestID)
	}

	mod, ok := manager.Get(cmd.Plugin)
	if !ok {
		sendBackupResult(w, cmd.Plugin, cmd.RequestID, "error", 0, 0, "プラグインが見つかりません", lg)
		return
	}

	runner, ok := mod.(module.BackupRunner)
	if !ok {
		sendBackupResult(w, cmd.Plugin, cmd.RequestID, "error", 0, 0, "BackupRunner 未実装", lg)
		return
	}

	go func() {
		// プラグインの panic を回復し、クラッシュするバックアッププラグインが
		// agent を倒さないようにする。代わりに通常のエラー結果として報告する。
		// Recover a plugin panic so a crashing backup plugin does not take
		// down the agent; report it as a normal error result instead.
		defer func() {
			if r := recover(); r != nil {
				if lg != nil {
					lg.ErrorT("agent.backup_panic", r)
				}
				sendBackupResult(w, cmd.Plugin, cmd.RequestID, "error", 0, 0, fmt.Sprintf("plugin panic: %v", r), lg)
			}
		}()
		start := time.Now()
		err := runner.RunBackup(ctx)
		duration := time.Since(start).Milliseconds()

		if err != nil {
			sendBackupResult(w, cmd.Plugin, cmd.RequestID, "error", 0, duration, err.Error(), lg)
			return
		}

		var size int64
		if provider, ok := mod.(module.BackupStatusProvider); ok {
			if st := provider.GetBackupStatus(); st != nil {
				size = st.Size
			}
		}
		sendBackupResult(w, cmd.Plugin, cmd.RequestID, "success", size, duration, "", lg)
	}()
}

// sendBackupResult は実行結果を Dashboard に返す。
// sendBackupResult returns the run result to the Dashboard.
func sendBackupResult(
	w *wsWriter,
	plugin, requestID, status string,
	size, durationMs int64,
	errMsg string,
	lg *logger.Logger,
) {
	payload := map[string]interface{}{
		"event":       "backup_result",
		"plugin":      plugin,
		"request_id":  requestID,
		"status":      status,
		"size":        size,
		"duration_ms": durationMs,
		"timestamp":   time.Now().Format(time.RFC3339),
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	data, err := json.Marshal(payload)
	if err != nil {
		if lg != nil {
			lg.ErrorT("agent.backup_json_failed", err)
		}
		return
	}
	if err := w.WriteMessage(websocket.TextMessage, data); err != nil {
		if lg != nil {
			lg.ErrorT("agent.backup_send_failed", err)
		}
		return
	}
	if lg != nil {
		lg.InfoT("agent.manual_sent", plugin, status, durationMs)
	}
}
