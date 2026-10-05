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

// checkDependencies verifies that external tools the agent relies on are
// installed. Missing tools are warned (not fatal): rsync is needed by backup
// plugins, smartctl by the S.M.A.R.T disk health collection. The warning goes
// to both the log file and stderr so it is visible when started interactively
// or via a service manager.
func checkDependencies(lg *logger.Logger) {
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
	// names is keyed by the name a plugin actually registered under (its own
	// Name()), which is what manager.Get/Unregister use.
	names map[string]bool
	// byCfg maps a modules.json entry name to the name the plugin registered
	// under, so a hand-edited modules.json whose name differs from the
	// plugin's Name() can still be disabled/reconfigured correctly.
	byCfg map[string]string
}{
	names: make(map[string]bool),
	byCfg: make(map[string]string),
}

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

	// Default to INFO so DEBUG noise is not emitted unless explicitly
	// enabled via agent_config.json's log_level ("debug").
	level := logger.INFO
	if cfg.LogLevel != "" {
		level = logger.ParseLevel(cfg.LogLevel)
	}
	lg := logger.NewLogger(&logger.Options{
		LogFile: cfg.LogFile,
		Level:   level,
		Prefix:  "[AGENT]",
		UseUTC:  false,
	})
	defer lg.Sync()

	lg.InfoT("agent.start", cfg.Interval, cfg.DashboardURL)

	// 起動時に外部ツールの有無を確認する（致命的ではない）。
	checkDependencies(lg)

	ctx, cancel := context.WithCancel(context.Background())

	// ---- 自動更新（GitHub Releases を監視し、新しければ safe_update.sh）----
	// セキュリティ注意: 自動更新は GitHub リポジトリ/アカウントが侵害されると、
	// サーバーが悪性コードを自動でビルド・実行する経路（実質 RCE）になる。
	// 既定は無効。信頼できるリポジトリでのみ有効化すること。
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
	// SystemConfig.Interval is in whole seconds. Truncating a sub-second
	// value (e.g. 0.5) to int would yield 0 and silently fall back to 1s,
	// so clamp to a minimum of 1 here and log the effective value.
	sysInterval := int(cfg.Interval)
	if sysInterval < 1 {
		sysInterval = 1
	}
	sysConfig := &module.SystemConfig{
		DiskPath:          cfg.DiskPath,
		Interval:          sysInterval,
		HealthInterval:    0,
		StaleThreshold:    0,
		ShutdownTimeout:   5,
		CPUErrorThreshold: 3,
	}
	sysModule := module.NewSystemModule(lg)
	if err := sysModule.Configure(sysConfig); err != nil {
		lg.ErrorT("agent.sys_module_cfg_failed", err)
	}
	if err := manager.Register(ctx, sysModule); err != nil {
		lg.ErrorT("agent.sys_module_reg_failed", err)
	}
	lg.InfoT("agent.sys_modules", sysInterval)

	// ---- Load plugins ----
	// Only .so files inside the configured plugins directory are allowed,
	// so a tampered modules.json cannot load an arbitrary library.
	pluginsDir := cfg.ResolvePluginsDir()
	lg.InfoT("agent.plugins_dir", pluginsDir)
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
func loadPluginsFromConfig(ctx context.Context, path string, manager module.ModuleManager, lg *logger.Logger, pluginsDir string, policy config.PluginSecurityConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			lg.InfoT("agent.modules_missing", path)
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

	// Track every plugin name still present in modules.json (enabled or not),
	// so a plugin that was deleted from the file can be stopped below.
	present := make(map[string]bool, len(configs))

	for _, cfg := range configs {
		if cfg.Type != "plugin" {
			continue
		}
		present[cfg.Name] = true
		if !cfg.Enabled {
			// Stop a previously-loaded plugin when it is disabled, so it
			// stops executing without an agent restart. Resolve the name it
			// was actually registered under, in case modules.json's name and
			// the plugin's own Name() differ.
			tracked := cfg.Name
			if !loadedPlugins.names[tracked] {
				if reg, ok := loadedPlugins.byCfg[cfg.Name]; ok {
					tracked = reg
				} else if mod, ok := manager.Get(cfg.Name); ok {
					tracked = mod.Name()
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
			continue
		}
		// Resolve the name this plugin actually registered under (its own
		// Name()), which may differ from the modules.json entry name.
		regName := cfg.Name
		if reg, ok := loadedPlugins.byCfg[cfg.Name]; ok {
			regName = reg
		}
		if loadedPlugins.names[regName] {
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
			continue
		}

		pluginPath, ok := cfg.Config["plugin_path"].(string)
		if !ok || pluginPath == "" {
			lg.ErrorT("agent.plugin_path_invalid", cfg.Name)
			continue
		}

		safePath, err := resolveAndCheckPluginPath(pluginPath, pluginsDir)
		if err != nil {
			lg.ErrorT("agent.plugin_rejected", cfg.Name, err)
			continue
		}

		registeredName, err := loadAndRegisterPlugin(ctx, safePath, cfg.Config, manager, lg, policy)
		if err != nil {
			lg.ErrorT("agent.plugin_load_failed", cfg.Name, err)
			continue
		}
		if registeredName == "" {
			registeredName = cfg.Name
		}
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
	}

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
	sigPath := pluginsig.SigPath(soPath)
	if err := pluginsig.VerifyFile(soPath, sigPath, pub); err != nil {
		if errors.Is(err, pluginsig.ErrNoSignature) {
			return fmt.Errorf("未署名のプラグインを拒否しました（%s がありません。plugin-sign で署名してください）: %w", sigPath, err)
		}
		return fmt.Errorf("プラグイン署名の検証に失敗しました（改ざんまたは鍵不一致の可能性）: %w", err)
	}
	if lg != nil {
		lg.InfoT("agent.plugin_sig_ok", soPath)
	}
	return nil
}

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
	// A-3: authenticate the .so before it is mapped into this process. This is
	// the choke point for every load path (initial load and modules.json
	// reload), so a plugin that never reached a verified state cannot execute
	// even if the dashboard's upload checks were bypassed.
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

	pluginMod, err := constructPlugin(newFunc, lg)
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

	return pluginMod.Name(), nil
}

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
	// A constructor that returns nil would make manager.Register call
	// module.Name() on a nil interface and crash the agent; reject it here.
	if mod == nil {
		return nil, fmt.Errorf("plugin constructor returned nil")
	}
	return mod, nil
}

// configurePlugin applies plugin config with panic recovery.
func configurePlugin(configurable interface{ Configure(interface{}) error }, config map[string]interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin Configure panicked: %v", r)
		}
	}()
	return configurable.Configure(config)
}

// watchModulesFile watches modules.json for changes and reloads.
func watchModulesFile(ctx context.Context, path string, manager module.ModuleManager, lg *logger.Logger, pluginsDir string, policy config.PluginSecurityConfig) {
	var lastMod time.Time

	if info, err := os.Stat(path); err == nil {
		lastMod = info.ModTime()
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if !info.ModTime().After(lastMod) {
				continue
			}

			lastMod = info.ModTime()
			lg.InfoT("agent.modules_changed")

			if err := loadPluginsFromConfig(ctx, path, manager, lg, pluginsDir, policy); err != nil {
				lg.ErrorT("agent.plugin_reload_failed", err)
			}
		}
	}
}

// ============================================================
// runAgent: Phase 8-5 で読み取りループを追加
// ============================================================
func runAgent(ctx context.Context, cfg *config.AgentConfig, lg *logger.Logger, manager module.ModuleManager) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	// Send the shared agent token as both a header and a query parameter.
	// The dashboard prefers the header; the query parameter exists only for
	// environments where a proxy strips custom headers. When the token is
	// empty, the dashboard falls back to heuristic detection (legacy).
	dialURL := cfg.DashboardURL
	header := http.Header{}
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

	// Serialize all writes to this connection.
	writer := &wsWriter{conn: conn}

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
	// Guard against a sub-nanosecond interval rounding to 0, which would make
	// time.NewTicker panic ("non-positive interval"). validateAgentConfig only
	// rejects interval <= 0, so a tiny positive value can still reach here.
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastSent, err := sendStatus(writer, manager, lg)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Security events are flushed every tick so their delivery latency
			// is not tied to the status collection interval.
			sendSecurityEvents(writer, manager, lg)

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
			if ev.TitleEN != "" {
				payload["title_en"] = ev.TitleEN
			}
			if ev.MessageEN != "" {
				payload["message_en"] = ev.MessageEN
			}
			// 詳細表示用フィールド (タスク1)。空のときは送らない。
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
				// The events were already drained from the plugin. Put the
				// unsent remainder back so a transient write error does not
				// silently drop security events (SSH brute force, FIM, ...).
				if requeuer, ok := provider.(module.SecurityEventRequeuer); ok {
					requeuer.RequeueSecurityEvents(events[i:])
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
// statusTimestamp returns the system module's latest status timestamp without
// copying it, so the send loop can cheaply detect an unchanged status.
func statusTimestamp(manager module.ModuleManager) int64 {
	sysMod, ok := manager.Get("system")
	if !ok {
		return 0
	}
	sysModule, ok := sysMod.(*module.SystemModule)
	if !ok {
		return 0
	}
	return sysModule.StatusTimestamp()
}

// sendStatus はステータスを WebSocket で送信し、送信したステータスの
// タイムスタンプを返す（未送信なら 0）。
// ============================================================
func sendStatus(w *wsWriter, manager module.ModuleManager, lg *logger.Logger) (int64, error) {
	sysMod, ok := manager.Get("system")
	if !ok {
		return 0, nil
	}
	sysModule, ok := sysMod.(*module.SystemModule)
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

// ============================================================
// collectBackupStatus
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
// Command handling from the dashboard.
// ============================================================

// handleDashboardCommand は Dashboard から受信したコマンドを処理する。
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
