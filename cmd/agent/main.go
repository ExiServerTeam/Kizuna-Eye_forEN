package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"plugin"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/logger"
	"Kizuna-Eye/pkg/module"
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		return &bytes.Buffer{}
	},
}

var loadedPlugins = struct {
	sync.Mutex
	names map[string]bool
}{
	names: make(map[string]bool),
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

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	manager := module.NewModuleManager(lg)

	// ---- システムモジュール ----
	sysInterval := int(cfg.Interval)
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

	// ---- プラグイン読み込み ----
	if err := loadPluginsFromConfig(ctx, *modulesPath, manager, lg); err != nil {
		lg.Error("プラグイン読み込みエラー: %v", err)
	}

	manager.Start(ctx)

	go watchModulesFile(ctx, *modulesPath, manager, lg)

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

// loadPluginsFromConfig は modules.json を読み込んでプラグインを登録する
func loadPluginsFromConfig(ctx context.Context, path string, manager module.ModuleManager, lg *logger.Logger) error {
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
			lg.Info("プラグイン '%s' は無効のためスキップ", cfg.Name)
			continue
		}
		if loadedPlugins.names[cfg.Name] {
			lg.Debug("プラグイン '%s' は既に読み込み済み", cfg.Name)
			continue
		}

		pluginPath, ok := cfg.Config["plugin_path"].(string)
		if !ok || pluginPath == "" {
			lg.Error("プラグイン '%s' の plugin_path が不正です", cfg.Name)
			continue
		}

		if err := loadAndRegisterPlugin(ctx, pluginPath, cfg.Config, manager, lg); err != nil {
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

// watchModulesFile は modules.json の変更を監視して再読み込みする
func watchModulesFile(ctx context.Context, path string, manager module.ModuleManager, lg *logger.Logger) {
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

			if err := loadPluginsFromConfig(ctx, path, manager, lg); err != nil {
				lg.Error("プラグイン再読み込み失敗: %v", err)
			}
		}
	}
}

// runAgent は WebSocket 接続を確立し、定期的にステータスを送信する
func runAgent(ctx context.Context, cfg *config.AgentConfig, lg *logger.Logger, manager module.ModuleManager) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	conn, _, err := dialer.Dial(cfg.DashboardURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	lg.Info("WebSocket 接続確立")

	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})

	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
					lg.Debug("Ping 送信失敗: %v", err)
					return
				}
			}
		}
	}()

	interval := time.Duration(cfg.Interval * float64(time.Second))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if err := sendStatus(conn, manager, lg); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := sendStatus(conn, manager, lg); err != nil {
				lg.Error("送信失敗: %v", err)
				return err
			}
		}
	}
}

// ============================================================
// sendStatus はステータスを WebSocket で送信する
//
// 注意: pkg/status/model.go の SystemStatus.Backup が匿名構造体として
// 定義されているため、フィールド単位で値をコピーする。
// ============================================================
func sendStatus(conn *websocket.Conn, manager module.ModuleManager, lg *logger.Logger) error {
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

	// バックアップモジュールの状態を集約し、匿名構造体のフィールドにコピー
	if bs := collectBackupStatus(manager); bs != nil {
		s.Backup.LastRun = bs.LastRun
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

	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, buf.Bytes()); err != nil {
		return err
	}

	lg.Debug("Status sent successfully")
	return nil
}

// ============================================================
// collectBackupStatus は全プラグインの中からバックアップ情報を持つものを探し、
// 最新のものを1つ返す。バックアップモジュールが無い場合は nil を返す。
//
// manager.List() を追加せず、main.go の loadedPlugins.names を利用する。
// ============================================================
func collectBackupStatus(manager module.ModuleManager) *module.BackupStatus {
	// 読み込み済みプラグイン名を取得
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

		// バックアップ対応モジュールかチェック
		provider, ok := mod.(module.BackupStatusProvider)
		if !ok {
			continue
		}

		status := provider.GetBackupStatus()
		if status == nil {
			continue
		}

		// まだ一度も実行していない場合は last_run が空
		if status.LastRun == "" {
			if latest == nil {
				latest = status
			}
			continue
		}

		// last_run を RFC3339 でパースして最新のものを採用
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

// parseSize は "1.2 GB" 形式の文字列を int64 に変換する
// （バックアッププラグイン側でサイズ文字列を扱う場合に利用）
func parseSize(sizeStr string) int64 {
	if sizeStr == "" {
		return 0
	}
	parts := strings.Fields(sizeStr)
	if len(parts) != 2 {
		return 0
	}
	val, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0
	}
	unit := strings.ToUpper(parts[1])
	multipliers := map[string]int64{
		"B": 1, "KB": 1024, "MB": 1024 * 1024,
		"GB": 1024 * 1024 * 1024, "TB": 1024 * 1024 * 1024 * 1024,
	}
	mult, ok := multipliers[unit]
	if !ok {
		return 0
	}
	return int64(val * float64(mult))
}
