package main

import (
	"bytes"
	"context"
	"encoding/json"
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
				lg.Warn("%s", msg)
			}
			fmt.Fprintln(os.Stderr, "[AGENT] 警告: "+msg)
		}
	}

	if len(missing) > 0 {
		hint := "install.sh を実行するか、お使いのパッケージマネージャで導入してください"
		if lg != nil {
			lg.Warn("不足ツール: %v — %s", missing, hint)
		}
		fmt.Fprintf(os.Stderr, "[AGENT] 警告: 不足ツール: %v — %s\n", missing, hint)
	}
}

var loadedPlugins = struct {
	sync.Mutex
	names map[string]bool
}{
	names: make(map[string]bool),
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

	lg := logger.NewLogger(&logger.Options{
		LogFile: cfg.LogFile,
		Level:   logger.DEBUG,
		Prefix:  "[AGENT]",
		UseUTC:  false,
	})
	defer lg.Sync()

	lg.Info("エージェント起動 (送信間隔: %.1f秒, 接続先: %s)", cfg.Interval, cfg.DashboardURL)

	// 起動時に外部ツールの有無を確認する（致命的ではない）。
	checkDependencies(lg)

	ctx, cancel := context.WithCancel(context.Background())

	// ---- 自動更新（GitHub Releases を監視し、新しければ safe_update.sh）----
	if au := cfg.AutoUpdate; au.Enabled {
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
		lg.Error("システムモジュール設定失敗: %v", err)
	}
	if err := manager.Register(ctx, sysModule); err != nil {
		lg.Error("システムモジュール登録失敗: %v", err)
	}
	lg.Info("システムモジュール登録: 収集間隔=%d秒", sysInterval)

	// ---- Load plugins ----
	// Only .so files inside the configured plugins directory are allowed,
	// so a tampered modules.json cannot load an arbitrary library.
	pluginsDir := cfg.ResolvePluginsDir()
	lg.Info("プラグインディレクトリ: %s", pluginsDir)
	if err := loadPluginsFromConfig(ctx, *modulesPath, manager, lg, pluginsDir); err != nil {
		lg.Error("プラグイン読み込みエラー: %v", err)
	}

	manager.Start(ctx)

	go watchModulesFile(ctx, *modulesPath, manager, lg, pluginsDir)

	go func() {
		<-sigCh
		lg.Info("シャットダウンシグナル受信")
		cancel()
	}()

	for {
		select {
		case <-ctx.Done():
			lg.Info("エージェント終了")
			manager.Stop()
			return
		default:
		}
		if err := runAgent(ctx, cfg, lg, manager); err != nil {
			lg.Error("エージェント実行エラー: %v, 5秒後に再接続", err)
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
func loadPluginsFromConfig(ctx context.Context, path string, manager module.ModuleManager, lg *logger.Logger, pluginsDir string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			lg.Info("modules.json が見つかりません（スキップ）: %s", path)
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

	for _, cfg := range configs {
		if cfg.Type != "plugin" {
			continue
		}
		if !cfg.Enabled {
			// Stop a previously-loaded plugin when it is disabled, so it
			// stops executing without an agent restart.
			if loadedPlugins.names[cfg.Name] {
				if err := manager.Unregister(cfg.Name); err != nil {
					lg.Error("プラグイン '%s' の停止に失敗: %v", cfg.Name, err)
				} else {
					delete(loadedPlugins.names, cfg.Name)
					lg.Info("プラグイン '%s' を無効化し停止しました", cfg.Name)
				}
			} else {
				lg.Info("プラグイン '%s' は無効のためスキップ", cfg.Name)
			}
			continue
		}
		if loadedPlugins.names[cfg.Name] {
			// Already loaded: apply the new config to the running plugin
			// so changes like interval_sec take effect without a restart.
			if existing, ok := manager.Get(cfg.Name); ok {
				if configurable, ok := existing.(interface {
					Configure(config interface{}) error
				}); ok {
					if err := configurable.Configure(cfg.Config); err != nil {
						lg.Error("プラグイン '%s' の再設定失敗: %v", cfg.Name, err)
					} else {
						manager.NotifyConfigChanged(cfg.Name)
						lg.Info("プラグイン '%s' の設定を更新しました", cfg.Name)
					}
				}
			}
			continue
		}

		pluginPath, ok := cfg.Config["plugin_path"].(string)
		if !ok || pluginPath == "" {
			lg.Error("プラグイン '%s' の plugin_path が不正です", cfg.Name)
			continue
		}

		safePath, err := resolveAndCheckPluginPath(pluginPath, pluginsDir)
		if err != nil {
			lg.Error("プラグイン '%s' を拒否しました: %v", cfg.Name, err)
			continue
		}

		if err := loadAndRegisterPlugin(ctx, safePath, cfg.Config, manager, lg); err != nil {
			lg.Error("プラグイン '%s' の読み込み失敗: %v", cfg.Name, err)
			continue
		}

		loadedPlugins.names[cfg.Name] = true
		lg.Info("プラグイン '%s' を登録しました", cfg.Name)
	}

	return nil
}

// loadAndRegisterPlugin は .so を読み込んで ModuleManager に登録する
func loadAndRegisterPlugin(
	ctx context.Context,
	path string,
	config map[string]interface{},
	manager module.ModuleManager,
	lg *logger.Logger,
) error {
	p, err := plugin.Open(path)
	if err != nil {
		return fmt.Errorf("plugin.Open 失敗 (%s): %w", path, err)
	}

	sym, err := p.Lookup("NewPluginModule")
	if err != nil {
		return fmt.Errorf("NewPluginModule が見つかりません: %w", err)
	}

	newFunc, ok := sym.(func(module.Logger) module.PluginModule)
	if !ok {
		return fmt.Errorf("NewPluginModule のシグネチャが不正です")
	}

	pluginMod := newFunc(lg)

	if configurable, ok := pluginMod.(interface {
		Configure(config interface{}) error
	}); ok {
		if err := configurable.Configure(config); err != nil {
			return fmt.Errorf("プラグイン設定適用失敗: %w", err)
		}
	}

	if err := manager.Register(ctx, pluginMod); err != nil {
		return fmt.Errorf("モジュール登録失敗: %w", err)
	}

	return nil
}

// watchModulesFile watches modules.json for changes and reloads.
func watchModulesFile(ctx context.Context, path string, manager module.ModuleManager, lg *logger.Logger, pluginsDir string) {
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
			lg.Info("modules.json の変更を検知、再読み込みします")

			if err := loadPluginsFromConfig(ctx, path, manager, lg, pluginsDir); err != nil {
				lg.Error("プラグイン再読み込み失敗: %v", err)
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
	lg.Info("WebSocket 接続確立")

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
					lg.Debug("Ping 送信失敗: %v", err)
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
					lg.Debug("読み取りエラー: %v", err)
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
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if err := sendStatus(writer, manager, lg); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := sendStatus(writer, manager, lg); err != nil {
				lg.Error("送信失敗: %v", err)
				return err
			}
			sendSecurityEvents(writer, manager, lg)
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
		events := provider.DrainSecurityEvents()
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
					lg.Error("セキュリティイベント送信失敗: %v", err)
				}
				return
			}
			if lg != nil {
				lg.Info("セキュリティイベント送信: [%s] %s (%s)", ev.Level, ev.Title, ev.Category)
			}
		}
	}
}

// ============================================================
// sendStatus はステータスを WebSocket で送信する
// ============================================================
func sendStatus(w *wsWriter, manager module.ModuleManager, lg *logger.Logger) error {
	sysMod, ok := manager.Get("system")
	if !ok {
		return nil
	}
	sysModule, ok := sysMod.(*module.SystemModule)
	if !ok {
		return nil
	}

	s := sysModule.GetStatus()
	if s == nil {
		return nil
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
		return err
	}

	if err := w.WriteMessage(websocket.TextMessage, buf.Bytes()); err != nil {
		return err
	}

	lg.Debug("Status sent successfully")
	return nil
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

		status := provider.GetBackupStatus()
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
			lg.Debug("コマンドのパース失敗: %v", err)
		}
		return
	}

	if cmd.Action != "run_backup" {
		if lg != nil {
			lg.Debug("未対応のアクション: %s", cmd.Action)
		}
		return
	}

	if lg != nil {
		lg.Info("手動実行リクエスト受信: plugin=%s request_id=%s", cmd.Plugin, cmd.RequestID)
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
			lg.Error("backup_result のJSON化失敗: %v", err)
		}
		return
	}
	if err := w.WriteMessage(websocket.TextMessage, data); err != nil {
		if lg != nil {
			lg.Error("backup_result の送信失敗: %v", err)
		}
		return
	}
	if lg != nil {
		lg.Info("手動実行結果送信: plugin=%s status=%s duration=%dms", plugin, status, durationMs)
	}
}
