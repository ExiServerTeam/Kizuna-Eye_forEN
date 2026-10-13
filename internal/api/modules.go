package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"Kizuna-Eye/pkg/fsutil"
)

// ErrModuleNotFound is returned when a module name does not exist, so
// handlers can answer 404 instead of masking a save failure as "not found".
var ErrModuleNotFound = errors.New("module not found")

// ModuleConfig represents a module config.
type ModuleConfig struct {
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

// ModulesStorage stores module configs in a file.
type ModulesStorage struct {
	mu      sync.RWMutex
	path    string
	modules []ModuleConfig
}

// NewModulesStorage creates a ModulesStorage.
func NewModulesStorage(path string) *ModulesStorage {
	return &ModulesStorage{
		path:    path,
		modules: []ModuleConfig{},
	}
}

// Load reads the config file.
func (s *ModulesStorage) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.modules = []ModuleConfig{}
			return nil
		}
		return err
	}

	// Tighten the mode: modules.json can embed plugin paths and settings.
	// Best-effort; ignore failure on filesystems without chmod support.
	_ = os.Chmod(s.path, 0600)

	// Treat an empty file as an empty list.
	if len(data) == 0 {
		s.modules = []ModuleConfig{}
		return nil
	}

	return json.Unmarshal(data, &s.modules)
}

// Save writes the config file atomically.
// It writes to a temp file in the same directory and renames it into place,
// so a crash or concurrent reader never observes a partially written file
// (matching the behaviour of ConfigHandler.saveJSONFile).
func (s *ModulesStorage) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked writes the current modules to disk. The caller must hold s.mu.
func (s *ModulesStorage) saveLocked() error {
	// Ensure the directory exists.
	dir := filepath.Dir(s.path)
	// Keep the directory owner-only alongside the 0600 config file.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.modules, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// Default to 0600 (owner only) and preserve an existing mode when the
	// file already exists.
	mode := os.FileMode(0600)
	if info, statErr := os.Stat(s.path); statErr == nil {
		mode = info.Mode().Perm()
	}

	// fsutil.WriteFileAtomic: 一時ファイルに mode を適用し fsync してから
	// rename する（L-12: 同じ処理が10箇所に重複していた）。上のディレクトリ
	// 作成 (0700) はそのまま残している（fsutil も作成するが冪等）。
	return fsutil.WriteFileAtomic(s.path, data, mode)
}

// GetAll returns all module configs.
func (s *ModulesStorage) GetAll() []ModuleConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ModuleConfig, len(s.modules))
	copy(result, s.modules)
	return result
}

// Get returns the module config with the given name.
func (s *ModulesStorage) Get(name string) *ModuleConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, mod := range s.modules {
		if mod.Name == name {
			copied := mod
			return &copied
		}
	}
	return nil
}

// Add appends a module.
// moduleNameRe matches a safe module identifier. Module names appear in API
// paths and log lines, and for plugins they become file names, so the same
// character set the plugin upload path enforces is required here too. Without
// this, a name like "a/b" or ".." could be stored and later used to build a
// file path or to confuse the UI.
var moduleNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateModuleName reports whether name is an acceptable module identifier.
func ValidateModuleName(name string) error {
	if name == "" {
		return fmt.Errorf("モジュール名が空です")
	}
	if len(name) > 64 {
		return fmt.Errorf("モジュール名が長すぎます（64文字以内）: %d", len(name))
	}
	if !moduleNameRe.MatchString(name) {
		return fmt.Errorf("モジュール名に使用できるのは英数字・ハイフン・アンダースコアのみです: %s", name)
	}
	return nil
}

func (s *ModulesStorage) Add(mod ModuleConfig) error {
	if err := ValidateModuleName(mod.Name); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for duplicates.
	for _, existing := range s.modules {
		if existing.Name == mod.Name {
			return fmt.Errorf("モジュール '%s' は既に存在します", mod.Name)
		}
	}

	s.modules = append(s.modules, mod)
	// Persist together with the mutation so a failed save cannot leave the
	// in-memory list out of sync with the file. Roll back on failure.
	if err := s.saveLocked(); err != nil {
		s.modules = s.modules[:len(s.modules)-1]
		return err
	}
	return nil
}

// Update replaces a module.
func (s *ModulesStorage) Update(name string, mod ModuleConfig) error {
	if name == "" {
		return fmt.Errorf("モジュール名は必須です")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.modules {
		if existing.Name == name {
			// The name is immutable.
			mod.Name = name
			// Guard against accidental config loss: an omitted/empty config
			// (e.g. a toggle request) keeps the existing one instead of
			// wiping the plugin settings.
			if isEmptyConfig(mod.Config) {
				mod.Config = existing.Config
			}
			if mod.Type == "" {
				mod.Type = existing.Type
			}
			prev := s.modules[i]
			s.modules[i] = mod
			if err := s.saveLocked(); err != nil {
				s.modules[i] = prev
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("%w: モジュール '%s' が見つかりません", ErrModuleNotFound, name)
}

// isEmptyConfig reports whether the raw config is empty or an empty object.
func isEmptyConfig(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return true
	}
	return string(trimmed) == "{}" || string(trimmed) == "null"
}

// Delete removes a module.
func (s *ModulesStorage) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, mod := range s.modules {
		if mod.Name == name {
			prev := s.modules[i]
			s.modules = append(s.modules[:i], s.modules[i+1:]...)
			if err := s.saveLocked(); err != nil {
				// Restore the removed entry at its original position.
				s.modules = append(s.modules, ModuleConfig{})
				copy(s.modules[i+1:], s.modules[i:])
				s.modules[i] = prev
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("%w: モジュール '%s' が見つかりません", ErrModuleNotFound, name)
}

// ---------- HTTP handlers ----------

// ModuleHandler serves the module management API.
type ModuleHandler struct {
	storage *ModulesStorage
	// pluginsDir is where a plugin's .so lives. It is used to remove the
	// runnable artifact when a plugin module is deleted, so a later upload
	// with the same name cannot silently overwrite it and so no orphan .so
	// stays loadable after the module is gone.
	pluginsDir string
}

// NewModuleHandler creates a ModuleHandler.
// pluginsDir may be empty, in which case only the modules.json entry is
// removed (no file cleanup).
func NewModuleHandler(storage *ModulesStorage, pluginsDir string) *ModuleHandler {
	return &ModuleHandler{storage: storage, pluginsDir: pluginsDir}
}

// RegisterRoutes registers the module routes.
func (h *ModuleHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/modules", h.handleGetAll)
	mux.HandleFunc("POST /api/modules", h.handleCreate)
	mux.HandleFunc("PUT /api/modules/{name}", h.handleUpdate)
	mux.HandleFunc("DELETE /api/modules/{name}", h.handleDelete)
	mux.HandleFunc("POST /api/modules/reload", h.handleReload)
}

// handleGetAll returns all modules.
func (h *ModuleHandler) handleGetAll(w http.ResponseWriter, r *http.Request) {
	modules := h.storage.GetAll()
	writeJSON(w, http.StatusOK, modules)
}

// handleCreate creates a module.
func (h *ModuleHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var mod ModuleConfig
	if err := json.NewDecoder(r.Body).Decode(&mod); err != nil {
		writeJSONError(w, http.StatusBadRequest, "リクエストボディのパースに失敗しました: "+err.Error())
		return
	}

	// Add persists internally and rolls back on save failure.
	if err := h.storage.Add(mod); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, mod)
}

// handleUpdate updates a module.
func (h *ModuleHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "モジュール名が指定されていません")
		return
	}

	var mod ModuleConfig
	if err := json.NewDecoder(r.Body).Decode(&mod); err != nil {
		writeJSONError(w, http.StatusBadRequest, "リクエストボディのパースに失敗しました: "+err.Error())
		return
	}

	// Update persists internally and rolls back on save failure.
	if err := h.storage.Update(name, mod); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrModuleNotFound) {
			status = http.StatusNotFound
		}
		writeJSONError(w, status, err.Error())
		return
	}

	updated := h.storage.Get(name)
	writeJSON(w, http.StatusOK, updated)
}

// handleDelete deletes a module.
func (h *ModuleHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "モジュール名が指定されていません")
		return
	}

	// Capture the module's plugin path before deletion so its .so can be
	// removed too. A module entry removed from modules.json while its .so
	// stays on disk is dangerous: a later upload with the same name would
	// pass the duplicate check and overwrite the installed plugin.
	var pluginPath string
	if mod := h.storage.Get(name); mod != nil && len(mod.Config) > 0 {
		var cfg struct {
			PluginPath string `json:"plugin_path"`
		}
		if json.Unmarshal(mod.Config, &cfg) == nil {
			pluginPath = cfg.PluginPath
		}
	}

	// Delete persists internally and rolls back on save failure.
	if err := h.storage.Delete(name); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrModuleNotFound) {
			status = http.StatusNotFound
		}
		writeJSONError(w, status, err.Error())
		return
	}

	// Remove the runnable artifacts. Only paths inside pluginsDir are
	// touched, so a crafted plugin_path cannot delete an arbitrary file.
	if h.pluginsDir != "" {
		candidates := []string{}
		if pluginPath != "" {
			candidates = append(candidates, pluginPath)
		}
		candidates = append(candidates,
			filepath.Join(h.pluginsDir, name+".so"),
			filepath.Join(h.pluginsDir, name+".meta.json"),
		)
		for _, p := range candidates {
			abs, err := filepath.Abs(p)
			if err != nil {
				continue
			}
			dir, err := filepath.Abs(h.pluginsDir)
			if err != nil {
				continue
			}
			if !strings.HasPrefix(abs, dir+string(os.PathSeparator)) {
				continue
			}
			_ = os.Remove(abs)
		}
		_ = os.RemoveAll(filepath.Join(h.pluginsDir, name+".web"))
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// handleReload reloads the config (hot reload).
func (h *ModuleHandler) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := h.storage.Load(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "再読み込みに失敗しました: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// ---------- Helpers ----------

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	// Keep session-scoped API responses (module configs, plugin metadata,
	// log/alert data) out of shared proxy and browser caches.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
