package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// ModuleConfig はモジュール設定を表す
type ModuleConfig struct {
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

// ModulesStorage はモジュール設定をファイルに保存する
type ModulesStorage struct {
	mu      sync.RWMutex
	path    string
	modules []ModuleConfig
}

// NewModulesStorage は新しいストレージを作成する
func NewModulesStorage(path string) *ModulesStorage {
	return &ModulesStorage{
		path:    path,
		modules: []ModuleConfig{},
	}
}

// Load は設定ファイルを読み込む
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

	// 空ファイルの場合は空配列として扱う
	if len(data) == 0 {
		s.modules = []ModuleConfig{}
		return nil
	}

	return json.Unmarshal(data, &s.modules)
}

// Save は設定ファイルを保存する
func (s *ModulesStorage) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// ディレクトリが存在することを確認
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.modules, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.path, data, 0644)
}

// GetAll はすべてのモジュール設定を返す
func (s *ModulesStorage) GetAll() []ModuleConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ModuleConfig, len(s.modules))
	copy(result, s.modules)
	return result
}

// Get は指定された名前のモジュール設定を返す
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

// Add はモジュールを追加する
func (s *ModulesStorage) Add(mod ModuleConfig) error {
	if mod.Name == "" {
		return fmt.Errorf("モジュール名は必須です")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 重複チェック
	for _, existing := range s.modules {
		if existing.Name == mod.Name {
			return fmt.Errorf("モジュール '%s' は既に存在します", mod.Name)
		}
	}

	s.modules = append(s.modules, mod)
	return nil
}

// Update はモジュールを更新する
func (s *ModulesStorage) Update(name string, mod ModuleConfig) error {
	if name == "" {
		return fmt.Errorf("モジュール名は必須です")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.modules {
		if existing.Name == name {
			// 名前は変更不可（既存の名前を維持）
			mod.Name = name
			s.modules[i] = mod
			return nil
		}
	}
	return fmt.Errorf("モジュール '%s' が見つかりません", name)
}

// Delete はモジュールを削除する
func (s *ModulesStorage) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, mod := range s.modules {
		if mod.Name == name {
			s.modules = append(s.modules[:i], s.modules[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("モジュール '%s' が見つかりません", name)
}

// ---------- HTTPハンドラ ----------

// ModuleHandler はモジュール管理のHTTPハンドラを提供する
type ModuleHandler struct {
	storage *ModulesStorage
}

// NewModuleHandler は新しいハンドラを作成する
func NewModuleHandler(storage *ModulesStorage) *ModuleHandler {
	return &ModuleHandler{storage: storage}
}

// RegisterRoutes はルートを登録する
func (h *ModuleHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/modules", h.handleGetAll)
	mux.HandleFunc("POST /api/modules", h.handleCreate)
	mux.HandleFunc("PUT /api/modules/{name}", h.handleUpdate)
	mux.HandleFunc("DELETE /api/modules/{name}", h.handleDelete)
	mux.HandleFunc("POST /api/modules/reload", h.handleReload)
}

// handleGetAll は全モジュール一覧を返す
func (h *ModuleHandler) handleGetAll(w http.ResponseWriter, r *http.Request) {
	modules := h.storage.GetAll()
	writeJSON(w, http.StatusOK, modules)
}

// handleCreate は新規モジュールを作成する
func (h *ModuleHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var mod ModuleConfig
	if err := json.NewDecoder(r.Body).Decode(&mod); err != nil {
		writeJSONError(w, http.StatusBadRequest, "リクエストボディのパースに失敗しました: "+err.Error())
		return
	}

	if err := h.storage.Add(mod); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.storage.Save(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "保存に失敗しました: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, mod)
}

// handleUpdate はモジュールを更新する
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

	if err := h.storage.Update(name, mod); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	if err := h.storage.Save(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "保存に失敗しました: "+err.Error())
		return
	}

	updated := h.storage.Get(name)
	writeJSON(w, http.StatusOK, updated)
}

// handleDelete はモジュールを削除する
func (h *ModuleHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "モジュール名が指定されていません")
		return
	}

	if err := h.storage.Delete(name); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	if err := h.storage.Save(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "保存に失敗しました: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// handleReload は設定を再読み込みする（ホットリロード）
func (h *ModuleHandler) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := h.storage.Load(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "再読み込みに失敗しました: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// ---------- ヘルパー ----------

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
