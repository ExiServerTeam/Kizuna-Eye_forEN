package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/module"
)

// runGuardTimeout is the safety valve for the server-side double-run guard.
// If a backup_result never arrives (Agent crash, network loss), the running
// flag is released after this duration so the plugin is not blocked forever.
const runGuardTimeout = 30 * time.Minute

// ============================================================
// AgentHub sends commands to the Agent.
// ============================================================
type AgentHub interface {
	SendToAgent(payload map[string]interface{}) error
}

// ============================================================
// PluginManager manages plugins.
// ============================================================
type PluginManager struct {
	storage       *ModulesStorage
	logger        module.Logger
	pluginsDir    string
	inspectBin    string
	hub           AgentHub
	uploadEnabled bool

	// Server-side double-run guard. The frontend also disables the button,
	// but that does not cover separate tabs/browsers, so the server refuses
	// a second run until the matching backup_result arrives (or times out).
	runMu   sync.Mutex
	running map[string]runGuard
}

// runGuard records an in-flight manual run so its backup_result can be matched
// by request_id. Matching prevents an unrelated (or scheduled) result from
// releasing the guard and letting a second run start while the first is still
// executing.
type runGuard struct {
	requestID string
	startedAt time.Time
}

// PluginMeta is the content of plugins/{name}.meta.json.
type PluginMeta struct {
	Name         string               `json:"name"`
	DisplayName  string               `json:"display_name,omitempty"`
	SOFilename   string               `json:"so_filename"`
	Description  string               `json:"description,omitempty"`
	IntervalSec  float64              `json:"interval_sec,omitempty"`
	Fields       []module.ConfigField `json:"fields"`
	IsBackup     bool                 `json:"is_backup"`
	UploadedAt   string               `json:"uploaded_at"`
	UploaderIP   string               `json:"uploader_ip,omitempty"`
	InspectError string               `json:"inspect_error,omitempty"`
	HasWebUI     bool                 `json:"has_web_ui,omitempty"`
}

// NewPluginManager creates a PluginManager.
// hub sends commands to the Agent; nil disables manual runs.
func NewPluginManager(storage *ModulesStorage, cfg *config.DashboardConfig, logger module.Logger, hub AgentHub) (*PluginManager, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("実行ファイルパス取得失敗: %w", err)
	}
	baseDir := filepath.Dir(exePath)

	// Resolve the plugins directory from config (fallback: plugins/ next to the executable).
	pluginsDir := filepath.Join(baseDir, "plugins")
	uploadEnabled := false
	if cfg != nil {
		if dir, err := cfg.EnsurePluginsDir(); err == nil {
			pluginsDir = dir
		}
		uploadEnabled = cfg.IsUploadEnabled()
	}
	// The directory holds executable .so files; keep it owner-only.
	if err := os.MkdirAll(pluginsDir, 0o700); err != nil {
		return nil, fmt.Errorf("plugins ディレクトリ作成失敗: %w", err)
	}

	inspectBin := filepath.Join(baseDir, "plugin-inspect")
	if _, err := os.Stat(inspectBin); err != nil {
		return nil, fmt.Errorf("plugin-inspect が見つかりません: %s (%w)", inspectBin, err)
	}

	return &PluginManager{
		storage:       storage,
		logger:        logger,
		pluginsDir:    pluginsDir,
		inspectBin:    inspectBin,
		hub:           hub,
		uploadEnabled: uploadEnabled,
		running:       make(map[string]runGuard),
	}, nil
}

// RegisterRoutes registers the plugin routes.
func (p *PluginManager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/plugins/upload", p.handleUpload)
	mux.HandleFunc("GET /api/plugins", p.handleList)
	mux.HandleFunc("DELETE /api/plugins/{name}", p.handleDelete)
	mux.HandleFunc("POST /api/plugins/{name}/run", p.handleRunPlugin) // ★ Phase 8-5
	// Serve plugin web UI from plugins/<name>.web/.
	mux.HandleFunc("GET /plugins/{name}/", p.handleWebUI)
}

// handleWebUI serves a plugin's bundled web UI.
// It serves files under plugins/<name>.web/, defaulting to index.html.
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

	// Resolve the path after /plugins/{name}/.
	rel := strings.TrimPrefix(r.URL.Path, "/plugins/"+name+"/")
	if rel == "" {
		rel = "index.html"
	}

	// Prevent path traversal.
	cleanRel := filepath.Clean("/" + rel)
	fullPath := filepath.Join(baseDir, cleanRel)
	if !strings.HasPrefix(fullPath, baseDir+string(os.PathSeparator)) && fullPath != baseDir {
		http.NotFound(w, r)
		return
	}

	// Plugin web UIs are third-party code and are served outside the main
	// static handler, so apply the same baseline security headers here.
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")

	// Append index.html for directory requests.
	if st, err := os.Stat(fullPath); err == nil && st.IsDir() {
		fullPath = filepath.Join(fullPath, "index.html")
	}

	http.ServeFile(w, r, fullPath)
}

var safeNameRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)

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

	// Only backup-capable plugins can be run manually. Watching plugins
	// (e.g. security) must not accept a run request; otherwise the Agent
	// would return an error for every click.
	if !p.isBackupPlugin(name) {
		writeJSONError(w, http.StatusBadRequest, "このプラグインは手動実行に対応していません。")
		return
	}

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
		// The request never reached the Agent; release the guard immediately.
		p.releaseRun(name, requestID)
		writeJSONError(w, http.StatusServiceUnavailable, "Agent に接続できません: "+err.Error())
		return
	}

	if p.logger != nil {
		p.logger.Info("手動実行リクエスト送信: plugin=%s request_id=%s", name, requestID)
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "accepted",
		"request_id": requestID,
		"plugin":     name,
	})
}

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
		// Stale entry: the result never came back. Allow a retry.
	}
	requestID = generateRequestID()
	p.running[name] = runGuard{requestID: requestID, startedAt: time.Now()}
	return requestID, true
}

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

// generateRequestID returns a random 16-byte ID.
func generateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

// ============================================================
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

	rawName := r.FormValue("name")
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
	if rawName == "" {
		rawName = strings.TrimSuffix(header.Filename, ".so")
	}
	name, err := sanitizeName(rawName)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	soPath := filepath.Join(p.pluginsDir, name+".so")

	// Use a unique temp file so concurrent uploads of the same plugin name
	// (or a stale .tmp left by a crash) cannot collide.
	out, err := os.CreateTemp(p.pluginsDir, name+".so.tmp-*")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "一時ファイル作成失敗: "+err.Error())
		return
	}
	tmpPath := out.Name()
	written, err := io.Copy(out, file)
	if cerr := out.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmpPath)
		writeJSONError(w, http.StatusInternalServerError, "ファイル書き込み失敗: "+err.Error())
		return
	}
	if err := os.Rename(tmpPath, soPath); err != nil {
		_ = os.Remove(tmpPath)
		writeJSONError(w, http.StatusInternalServerError, "リネーム失敗: "+err.Error())
		return
	}

	if p.logger != nil {
		p.logger.Info("プラグインアップロード: %s (%d bytes) from %s", name, written, r.RemoteAddr)
	}

	// Inspect with plugin-inspect.
	meta, err := p.inspectPlugin(r.Context(), name, soPath, r.RemoteAddr)
	if err != nil {
		if p.logger != nil {
			p.logger.Error("プラグイン検証失敗: %s: %v", name, err)
		}
		meta = &PluginMeta{
			Name:         name,
			SOFilename:   name + ".so",
			UploadedAt:   time.Now().Format(time.RFC3339),
			UploaderIP:   r.RemoteAddr,
			InspectError: err.Error(),
		}
	}

	// Save meta.json.
	metaPath := filepath.Join(p.pluginsDir, name+".meta.json")
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	// meta.json includes the uploader IP; keep it owner-only.
	if err := os.WriteFile(metaPath, metaBytes, 0o600); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "meta.json 書き込み失敗: "+err.Error())
		return
	}

	// Register in modules.json (build JSON safely).
	cfgBytes, err := json.Marshal(map[string]string{"plugin_path": soPath})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "設定JSON生成失敗: "+err.Error())
		return
	}
	modConfig := ModuleConfig{
		Name:    name,
		Type:    "plugin",
		Enabled: true,
		Config:  json.RawMessage(cfgBytes),
	}
	// Add persists internally and rolls back on save failure.
	if err := p.storage.Add(modConfig); err != nil {
		if p.logger != nil {
			p.logger.Warn("モジュール登録スキップ: %s: %v", name, err)
		}
	}

	writeJSON(w, http.StatusOK, meta)
}

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
		// Skip empty names and duplicates. Different meta files can point to the
		// same plugin name (e.g. a stray "Name.meta.json"); without this guard
		// the plugin would appear twice in the list.
		if meta.Name == "" || seen[meta.Name] {
			continue
		}
		seen[meta.Name] = true
		// Flag whether a web UI directory exists.
		webDir := filepath.Join(p.pluginsDir, meta.Name+".web")
		if st, err := os.Stat(webDir); err == nil && st.IsDir() {
			meta.HasWebUI = true
		}
		list = append(list, &meta)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"plugins": list,
		"dir":     p.pluginsDir,
	})
}

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

	// Delete persists internally and rolls back on save failure.
	if err := p.storage.Delete(name); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	_ = os.Remove(filepath.Join(p.pluginsDir, name+".so"))
	_ = os.Remove(filepath.Join(p.pluginsDir, name+".meta.json"))
	// Remove the plugin's bundled web UI directory, if any, so no stale
	// assets remain after deletion.
	_ = os.RemoveAll(filepath.Join(p.pluginsDir, name+".web"))

	if p.logger != nil {
		p.logger.Info("プラグインモジュール削除: %s", name)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// inspectPlugin runs plugin-inspect and parses the result.
func (p *PluginManager) inspectPlugin(
	parent context.Context, name, soPath, remoteAddr string,
) (*PluginMeta, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.inspectBin, "--so", soPath)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("plugin-inspect 失敗 (exit=%d): %s", ee.ExitCode(), string(ee.Stderr))
		}
		return nil, fmt.Errorf("plugin-inspect 実行失敗: %w", err)
	}

	var raw struct {
		Success      bool                 `json:"success"`
		Name         string               `json:"name"`
		DisplayName  string               `json:"display_name"`
		Description  string               `json:"description"`
		IntervalSec  float64              `json:"interval_sec"`
		Fields       []module.ConfigField `json:"fields"`
		IsBackup     bool                 `json:"is_backup"`
		ErrorMessage string               `json:"error_message"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("plugin-inspect の出力パース失敗: %w", err)
	}
	if !raw.Success {
		return nil, fmt.Errorf("plugin-inspect がエラーを返しました: %s", raw.ErrorMessage)
	}

	if raw.Name == "" {
		raw.Name = name
	}

	return &PluginMeta{
		Name:        raw.Name,
		DisplayName: raw.DisplayName,
		SOFilename:  name + ".so",
		Description: raw.Description,
		IntervalSec: raw.IntervalSec,
		Fields:      raw.Fields,
		IsBackup:    raw.IsBackup,
		UploadedAt:  time.Now().Format(time.RFC3339),
		UploaderIP:  remoteAddr,
	}, nil
}
