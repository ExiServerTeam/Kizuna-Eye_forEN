package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

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

	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
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
func (s *ModulesStorage) Add(mod ModuleConfig) error {
	if mod.Name == "" {
		return fmt.Errorf("モジュール名は必須です")
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
	return fmt.Errorf("モジュール '%s' が見つかりません", name)
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
	return fmt.Errorf("モジュール '%s' が見つかりません", name)
}

// ---------- HTTP handlers ----------

// ModuleHandler serves the module management API.
type ModuleHandler struct {
	storage *ModulesStorage
}

// NewModuleHandler creates a ModuleHandler.
func NewModuleHandler(storage *ModulesStorage) *ModuleHandler {
	return &ModuleHandler{storage: storage}
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
		writeJSONError(w, http.StatusNotFound, err.Error())
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

	// Delete persists internally and rolls back on save failure.
	if err := h.storage.Delete(name); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
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
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
