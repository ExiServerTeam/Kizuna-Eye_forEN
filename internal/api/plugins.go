package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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

	// audit, when set, receives security-relevant plugin actions (upload /
	// delete) so they are recorded in the alert history, not only logged.
	audit func(kind, detail string)
}

// SetAuditHook installs the callback for plugin audit events.
func (p *PluginManager) SetAuditHook(fn func(kind, detail string)) {
	p.audit = fn
}

// reportAudit invokes the audit hook, if any.
func (p *PluginManager) reportAudit(kind, detail string) {
	if p.audit != nil {
		p.audit(kind, detail)
	}
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

	// Append index.html for directory requests.
	if st, err := os.Stat(fullPath); err == nil && st.IsDir() {
		fullPath = filepath.Join(fullPath, "index.html")
	}

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
	// Reject anything that is not a regular file (devices, sockets, dirs).
	if st, err := os.Stat(realPath); err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	// Plugin web UIs are third-party code and are served outside the main
	// static handler, so apply the same baseline security headers here.
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")

	http.ServeFile(w, r, realPath)
}

// fileExists reports whether path exists (any type).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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

	// soPath/metaPath are finalized after plugin-inspect: the canonical
	// name comes from the plugin itself (see below).
	var soPath, metaPath string

	// Use a unique temp file so concurrent uploads of the same plugin name
	// (or a stale .tmp left by a crash) cannot collide. The upload stays in
	// the temp file until inspect + meta + modules.json all succeed, so a
	// failure never leaves an orphan, potentially-runnable .so behind.
	out, err := os.CreateTemp(p.pluginsDir, name+".so.tmp-*")
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
		p.logger.Info("プラグインアップロード受信: %s (%d bytes) from %s", name, written, r.RemoteAddr)
	}

	// Inspect the temp file BEFORE moving it into place. A plugin that cannot
	// be inspected must NOT be installed: plugin-inspect loads the .so to read
	// its metadata, so a failed inspect means the file is not a loadable Go
	// plugin (wrong arch, corrupt, or a non-plugin). Committing it would place
	// a broken/malicious .so on disk for the agent to load on the next restart.
	// Reject the upload and let the deferred cleanup remove the temp file.
	meta, err := p.inspectPlugin(r.Context(), name, tmpPath, r.RemoteAddr)
	if err != nil {
		if p.logger != nil {
			p.logger.Error("プラグイン検証失敗のため拒否: %s: %v", name, err)
		}
		writeJSONError(w, http.StatusBadRequest,
			"プラグインの検証に失敗したため登録できません（.so が壊れているか、対応していない形式です）: "+err.Error())
		return
	}

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

	// Refuse a duplicate BEFORE touching the installed plugin's files.
	// Two checks are needed:
	//   1. modules.json still lists it (normal case), and
	//   2. the .so / .meta.json exist on disk even if modules.json does not.
	// Without check 2, deleting a module from modules.json (a separate
	// operation) would let a later upload with the same name pass the
	// storage.Get test and overwrite the installed .so via os.Rename below,
	// destroying a working plugin.
	if p.storage.Get(canonical) != nil {
		writeJSONError(w, http.StatusConflict, "同名のプラグインが既に登録されています: "+canonical)
		return
	}
	meta.Name = canonical
	meta.SOFilename = canonical + ".so"
	soPath = filepath.Join(p.pluginsDir, canonical+".so")
	metaPath = filepath.Join(p.pluginsDir, canonical+".meta.json")
	if fileExists(soPath) || fileExists(metaPath) {
		writeJSONError(w, http.StatusConflict,
			"同名のプラグインファイルが既に存在します。先に削除してください: "+canonical)
		return
	}

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

	// Move the inspected .so into place, then write meta.json. If meta.json
	// cannot be written, roll back the .so so no orphan remains.
	if err := os.Rename(tmpPath, soPath); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "リネーム失敗: "+err.Error())
		return
	}
	committed = true

	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	// meta.json includes the uploader IP; keep it owner-only.
	if err := os.WriteFile(metaPath, metaBytes, 0o600); err != nil {
		_ = os.Remove(soPath)
		writeJSONError(w, http.StatusInternalServerError, "meta.json 書き込み失敗: "+err.Error())
		return
	}

	// Register in modules.json. Add persists internally and rolls back its
	// in-memory list on save failure; on failure we also remove the .so and
	// meta.json so the plugin is not left half-registered.
	if err := p.storage.Add(modConfig); err != nil {
		if p.logger != nil {
			p.logger.Warn("モジュール登録失敗: %s: %v", name, err)
		}
		_ = os.Remove(soPath)
		_ = os.Remove(metaPath)
		writeJSONError(w, http.StatusInternalServerError, "モジュール登録に失敗しました: "+err.Error())
		return
	}

	// Audit: installing a plugin is a code-execution boundary and must be
	// recorded in the alert history, not only logged.
	p.reportAudit("plugin_upload", "プラグイン '"+canonical+"' をアップロード・登録しました (from "+r.RemoteAddr+")")

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

	// Remove the .so first: it is the runnable artifact. If it cannot be
	// removed, report an error so the caller knows a runnable orphan may
	// remain on disk, instead of falsely claiming success.
	if err := os.Remove(filepath.Join(p.pluginsDir, name+".so")); err != nil && !os.IsNotExist(err) {
		if p.logger != nil {
			p.logger.Error("プラグイン .so の削除に失敗: %s: %v", name, err)
		}
		writeJSONError(w, http.StatusInternalServerError, ".so の削除に失敗しました: "+err.Error())
		return
	}
	_ = os.Remove(filepath.Join(p.pluginsDir, name+".meta.json"))
	// Remove the plugin's bundled web UI directory, if any, so no stale
	// assets remain after deletion.
	_ = os.RemoveAll(filepath.Join(p.pluginsDir, name+".web"))

	if p.logger != nil {
		p.logger.Info("プラグインモジュール削除: %s", name)
	}
	// Audit: removing a plugin is a code-execution boundary change.
	p.reportAudit("plugin_delete", "プラグイン '"+name+"' を削除しました (from "+r.RemoteAddr+")")

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
	} else if _, err := sanitizeName(raw.Name); err != nil {
		// The plugin self-reported a name that is not filesystem-safe
		// (e.g. contains a path separator). Fall back to the sanitized
		// upload name so meta.json cannot carry a path-traversal name.
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
