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

	"Kizuna-Eye/internal/pluginsig"
	"Kizuna-Eye/pkg/configsnap"
	"Kizuna-Eye/pkg/fsutil"
)

// ErrModuleNotFound はモジュール名が存在しないときに返され、ハンドラが保存失敗を
// 「見つからない」と偽るのではなく 404 を返せるようにする。
// ErrModuleNotFound is returned when a module name does not exist, so
// handlers can answer 404 instead of masking a save failure as "not found".
var ErrModuleNotFound = errors.New("module not found")

// ModuleConfig はモジュール設定を表す。
// ModuleConfig represents a module config.
type ModuleConfig struct {
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

// ModulesStorage はモジュール設定をファイルに保存する。
// ModulesStorage stores module configs in a file.
type ModulesStorage struct {
	mu      sync.RWMutex
	path    string
	modules []ModuleConfig
}

// NewModulesStorage は ModulesStorage を作る。
// NewModulesStorage creates a ModulesStorage.
func NewModulesStorage(path string) *ModulesStorage {
	return &ModulesStorage{
		path:    path,
		modules: []ModuleConfig{},
	}
}

// Load は設定ファイルを読む。
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

	// モードを締める。modules.json はプラグインパスや設定を埋め込み得る。
	// ベストエフォート。chmod 非対応のファイルシステムでの失敗は無視する。
	// Tighten the mode: modules.json can embed plugin paths and settings.
	// Best-effort; ignore failure on filesystems without chmod support.
	//
	// A-4: agent (kizuna-eye) は dashboard が保存のたびに書き換えるこの同じファイルを
	// 読む。意図的なグループ読み取りビット（0640、共有 kizuna-eye グループ）を保つ。
	// そうしないと agent は次の保存後にアクセスを失い、UI のプラグイン設定が
	// 適用されなくなる。
	// A-4: the agent (kizuna-eye) reads this same file, which the dashboard
	// rewrites on every save. A deliberate group-read bit (0640, shared
	// kizuna-eye group) is preserved, otherwise the agent would lose access
	// after the next save and the UI's plugin settings would stop applying.
	fsutil.TightenSharedConfigMode(s.path)

	// 空ファイルは空リストとして扱う。
	// Treat an empty file as an empty list.
	if len(data) == 0 {
		s.modules = []ModuleConfig{}
		return nil
	}

	// まず一時スライスへデコードする。json.Unmarshal はエラーを返す前に
	// 対象を部分的に埋め得るため、s.modules へ直接アンマーシャルすると
	// メモリ内リストが半端に更新されたまま残る（そして handleGetAll は
	// エラーを無視するので UI は壊れたデータを描画する）。完全に成功した
	// デコードだけが生きたリストを置き換える。
	// Decode into a temporary slice first. json.Unmarshal can partially
	// populate its target before returning an error, so unmarshalling
	// directly into s.modules would leave the in-memory list half-updated
	// (and handleGetAll ignores the error, so the UI would render broken
	// data). Only a fully successful decode replaces the live list.
	var parsed []ModuleConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	s.modules = parsed
	return nil
}

// Save は設定ファイルをアトミックに書く。
// 同一ディレクトリの一時ファイルへ書き、それを rename するため、クラッシュや
// 並行読み取りが部分的に書かれたファイルを観測することが決してない
// （ConfigHandler.saveJSONFile の挙動と一致）。
// Save writes the config file atomically.
// It writes to a temp file in the same directory and renames it into place,
// so a crash or concurrent reader never observes a partially written file
// (matching the behaviour of ConfigHandler.saveJSONFile).
func (s *ModulesStorage) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked は現在のモジュールをディスクへ書く。呼び出し側は s.mu を保持すること。
// saveLocked writes the current modules to disk. The caller must hold s.mu.
func (s *ModulesStorage) saveLocked() error {
	// ディレクトリの存在を保証する。
	// Ensure the directory exists.
	dir := filepath.Dir(s.path)
	// 0600 の設定ファイルと併せて、ディレクトリも所有者のみに保つ。
	// Keep the directory owner-only alongside the 0600 config file.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.modules, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// 既定は 0600（所有者のみ）とし、ファイルが既に存在すれば既存モードを保つ。
	// Default to 0600 (owner only) and preserve an existing mode when the
	// file already exists.
	mode := os.FileMode(0600)
	if info, statErr := os.Stat(s.path); statErr == nil {
		mode = info.Mode().Perm()
	}

	// タイムトラベル: 変更前ファイルのスナップショットを保ち、運用者が誤編集を
	// ロールバックできるようにする。ベストエフォート。スナップショット失敗が
	// 保存を妨げてはならない（書き込み自体は依然アトミックだ）。
	// Time-travel: keep a snapshot of the pre-change file so an operator can
	// roll back a bad edit. Best-effort: a snapshot failure must not block the
	// save (the write itself is still atomic).
	_ = configsnap.Snapshot(s.path)

	// fsutil.WriteFileAtomic: 一時ファイルに mode を適用し fsync してから
	// rename する（L-12: 同じ処理が10箇所に重複していた）。上のディレクトリ
	// 作成 (0700) はそのまま残している（fsutil も作成するが冪等）。
	// fsutil.WriteFileAtomic: apply the mode to a temp file, fsync, then
	// rename (L-12: the same logic was duplicated in ten places). The
	// directory creation (0700) above stays (fsutil also creates it, but it
	// is idempotent).
	return fsutil.WriteFileAtomic(s.path, data, mode)
}

// GetAll は全モジュール設定を返す。
// GetAll returns all module configs.
func (s *ModulesStorage) GetAll() []ModuleConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ModuleConfig, len(s.modules))
	copy(result, s.modules)
	return result
}

// Get は指定名のモジュール設定を返す。
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

// Add はモジュールを追記する。
// moduleNameRe は安全なモジュール識別子に一致する。モジュール名は API パスと
// ログ行に現れ、プラグインではファイル名になるため、プラグインアップロード経路が
// 強制するのと同じ文字集合をここでも要求する。これが無いと "a/b" や ".." の
// ような名前が保存され、後にファイルパスの構築や UI の混乱に使われ得る。
// Add appends a module.
// moduleNameRe matches a safe module identifier. Module names appear in API
// paths and log lines, and for plugins they become file names, so the same
// character set the plugin upload path enforces is required here too. Without
// this, a name like "a/b" or ".." could be stored and later used to build a
// file path or to confuse the UI.
var moduleNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateModuleName は name が許容されるモジュール識別子かを報告する。
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

	// 重複を確認する。
	// Check for duplicates.
	for _, existing := range s.modules {
		if existing.Name == mod.Name {
			return fmt.Errorf("モジュール '%s' は既に存在します", mod.Name)
		}
	}

	s.modules = append(s.modules, mod)
	// 変更と併せて永続化し、保存失敗がメモリ内リストをファイルと不整合のまま
	// 残さないようにする。失敗時はロールバックする。
	// Persist together with the mutation so a failed save cannot leave the
	// in-memory list out of sync with the file. Roll back on failure.
	if err := s.saveLocked(); err != nil {
		s.modules = s.modules[:len(s.modules)-1]
		return err
	}
	return nil
}

// Update はモジュールを置き換える。
// Update replaces a module.
func (s *ModulesStorage) Update(name string, mod ModuleConfig) error {
	if name == "" {
		return fmt.Errorf("モジュール名は必須です")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.modules {
		if existing.Name == name {
			// 名前は不変だ。
			// The name is immutable.
			mod.Name = name
			// 設定の偶発的な消失を防ぐ。省略/空の config（例: トグルのみの
			// リクエスト）はプラグイン設定を消すのではなく既存のものを保つ。
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

// isEmptyConfig は生の設定が空または空オブジェクトかを報告する。
// isEmptyConfig reports whether the raw config is empty or an empty object.
func isEmptyConfig(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return true
	}
	return string(trimmed) == "{}" || string(trimmed) == "null"
}

// Delete はモジュールを削除する。
// Delete removes a module.
func (s *ModulesStorage) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, mod := range s.modules {
		if mod.Name == name {
			prev := s.modules[i]
			s.modules = append(s.modules[:i], s.modules[i+1:]...)
			if err := s.saveLocked(); err != nil {
				// 削除したエントリを元の位置へ戻す。
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

// ---------- HTTP ハンドラ ----------
// ---------- HTTP handlers ----------

// ModuleHandler はモジュール管理 API を提供する。
// ModuleHandler serves the module management API.
type ModuleHandler struct {
	storage *ModulesStorage
	// pluginsDir はプラグインの .so が置かれる場所だ。プラグインモジュールの削除時に
	// 実行可能な成果物を除去するのに使う。これにより、同名の後日のアップロードが
	// 黙って上書きしたり、モジュール消滅後に孤立した .so がロード可能なまま
	// 残ったりしない。
	// pluginsDir is where a plugin's .so lives. It is used to remove the
	// runnable artifact when a plugin module is deleted, so a later upload
	// with the same name cannot silently overwrite it and so no orphan .so
	// stays loadable after the module is gone.
	pluginsDir string
	// adminChecker はリクエストが admin からのものかを報告する。dashboard が注入する
	// （api は auth をインポートしてはならない）。nil の checker は「admin でない」
	// （fail-closed）として扱い、配線漏れがコード実行境界の変更権限を広げることが
	// 決してないようにする。
	// adminChecker reports whether the request is from an admin. It is
	// injected by the dashboard (api must not import auth). A nil checker is
	// treated as "not admin" (fail-closed), so a missing wiring can never
	// widen the permission for a code-execution-boundary change.
	adminChecker func(*http.Request) bool
}

// NewModuleHandler は ModuleHandler を作る。
// pluginsDir は空でもよく、その場合は modules.json のエントリのみを除去する
// （ファイル清掃なし）。
// NewModuleHandler creates a ModuleHandler.
// pluginsDir may be empty, in which case only the modules.json entry is
// removed (no file cleanup).
func NewModuleHandler(storage *ModulesStorage, pluginsDir string) *ModuleHandler {
	return &ModuleHandler{storage: storage, pluginsDir: pluginsDir}
}

// SetAdminChecker はコード実行境界の操作（現在はモジュールの plugin_path 変更）に
// 使う admin 判定を注入する。起動時に呼ぶ。
// SetAdminChecker injects the admin test used for code-execution-boundary
// operations (currently: changing a module's plugin_path). Call during startup.
func (h *ModuleHandler) SetAdminChecker(fn func(*http.Request) bool) {
	h.adminChecker = fn
}

// pluginPathOf はモジュール設定 JSON から "plugin_path" を抜き出す。フィールドが
// 無ければ "" を返す。不正な設定は "" （無し扱い）を生む。
// pluginPathOf extracts "plugin_path" from a module config JSON, or "" when
// the field is absent. A malformed config yields "" (treated as absent).
func pluginPathOf(cfg json.RawMessage) string {
	if len(cfg) == 0 {
		return ""
	}
	var c struct {
		PluginPath string `json:"plugin_path"`
	}
	if json.Unmarshal(cfg, &c) != nil {
		return ""
	}
	return c.PluginPath
}

// adminOnlyConfigFields は保護・復旧機能、またはコード実行境界に触れるモジュール
// 設定キーを列挙する。これらの変更には admin が必要だ（docs/PROCEDURE.md 3.1 参照）。
// 他の設定は operator が編集できる。リストはソートしてあり、複数フィールドが
// 異なるときの変更検出が決定的になる。
// adminOnlyConfigFields lists module config keys that touch a protection or
// recovery capability, or a code-execution boundary. Changing any of them
// requires admin (see docs/PROCEDURE.md 3.1); other settings stay editable by
// operator. The list is sorted so change detection is deterministic when more
// than one field differs.
var adminOnlyConfigFields = []string{
	"block_duration_sec",
	"block_mode",
	"block_state_path",
	"block_whitelist",
	"chain_key_path",
	"cron_check",
	"cron_paths",
	"fim_watch",
	"fim_watch_baseline_path",
	"fim_watch_paths",
	"firewall_backend",
	"integrity_baseline_path",
	"integrity_files",
	"listen_port_check",
	"log_path",
	"notify_min_level",
	"plugin_path",
	"suid_check",
	"suid_fast_check",
	"suid_fast_paths",
	"suid_paths",
	"suid_watch",
	"suid_watch_paths",
	"sudo_ignore_commands",
	"watch_files",
}

// rawConfigMap はモジュール設定を生のキー/値ペアへデコードする。空または不正な
// 設定は nil（フィールド無し扱い）を生む。
// rawConfigMap decodes a module config into its raw key/value pairs. An empty
// or malformed config yields nil (treated as "no fields").
func rawConfigMap(cfg json.RawMessage) map[string]json.RawMessage {
	if len(cfg) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(cfg, &m) != nil {
		return nil
	}
	return m
}

// changedAdminOnlyField は old と new で値が異なる最初の admin 専用設定フィールド
// （ソート順）の名前を返し、無ければ "" を返す。old に存在し new に無い保護フィールドも
// 変更として数える。ModulesStorage.Update は設定を丸ごと置き換えるため（完全に空の
// 設定のみが古いものを保つ）、フィールドを省略するとそれを落としてしまう。比較は
// 生の JSON 値で行うため、あらゆる型変更が検出される。
// changedAdminOnlyField returns the name of the first admin-only config field
// (in sorted order) whose value differs between old and new, or "" when none
// did. A protection field that is present in old but absent from new counts as
// a change too: ModulesStorage.Update replaces the config wholesale (only an
// entirely empty config keeps the old one), so omitting the field would drop
// it. Comparison is by raw JSON value, so any type change is detected.
func changedAdminOnlyField(oldCfg, newCfg json.RawMessage) string {
	oldMap := rawConfigMap(oldCfg)
	newMap := rawConfigMap(newCfg)
	for _, key := range adminOnlyConfigFields {
		ov, oOK := oldMap[key]
		nv, nOK := newMap[key]
		if !nOK {
			if oOK {
				return key
			}
			continue
		}
		if !oOK || !bytes.Equal(ov, nv) {
			return key
		}
	}
	return ""
}

// RegisterRoutes はモジュールのルートを登録する。
// RegisterRoutes registers the module routes.
func (h *ModuleHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/modules", h.handleGetAll)
	mux.HandleFunc("POST /api/modules", h.handleCreate)
	mux.HandleFunc("PUT /api/modules/{name}", h.handleUpdate)
	mux.HandleFunc("DELETE /api/modules/{name}", h.handleDelete)
	mux.HandleFunc("POST /api/modules/reload", h.handleReload)
}

// handleGetAll は全モジュールを返す。
//
// 応答前に modules.json をディスクから再読み込みする。agent はファイルの mtime を
// 監視して再読み込みするが、dashboard はかつて起動時にロードしたスナップショットしか
// 保たなかったため、手で（または別プロセスが）modules.json へ追加したモジュールが
// agent には現れるが UI には現れなかった。ここで再読み込みすると 2 つのビューが
// 一致する。読み取りエラーはメモリ内スナップショットへフォールバックし、モジュール
// 一覧の描画が決して失敗しないようにする。
// handleGetAll returns all modules.
//
// It re-reads modules.json from disk before answering. The agent watches the
// file's mtime and reloads it, but the dashboard used to keep only the
// snapshot loaded at startup, so a module added to modules.json by hand (or by
// another process) appeared in the agent but not in the UI. Reloading here
// keeps the two views consistent; a read error falls back to the in-memory
// snapshot so the module list never fails to render.
func (h *ModuleHandler) handleGetAll(w http.ResponseWriter, r *http.Request) {
	_ = h.storage.Load()
	modules := h.storage.GetAll()
	writeJSON(w, http.StatusOK, modules)
}

// handleCreate はモジュールを作成する。
// handleCreate creates a module.
func (h *ModuleHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var mod ModuleConfig
	if err := json.NewDecoder(r.Body).Decode(&mod); err != nil {
		writeJSONError(w, http.StatusBadRequest, "リクエストボディのパースに失敗しました: "+err.Error())
		return
	}

	// Add は内部で永続化し、保存失敗時にロールバックする。
	// Add persists internally and rolls back on save failure.
	if err := h.storage.Add(mod); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, mod)
}

// handleUpdate はモジュールを更新する。
// handleUpdate updates a module.
func (h *ModuleHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// 使う前にパスセグメントを検証する。Go の ServeMux は {name} を単一の
	// （エスケープされた）セグメントにマッチするが、URL エンコードされた
	// スラッシュ（%2f）は値へデコードされるため、name は "/" や ".." を
	// 含み得る。handleDelete は name をファイルパス（name+".so"、name+".web"）へ
	// 変換するので、未検証の name はパストラバーサル / 任意削除の経路になる
	// （例: DELETE /api/modules/..%2f..%2fetc）。プラグインアップロード経路が
	// 使うのと同じ文字集合（ValidateModuleName / moduleNameRe）を強制する。
	// Validate the path segment before it is used. Go's ServeMux matches
	// {name} against a single (escaped) segment, but URL-encoded slashes
	// (%2f) are decoded into the value, so name can contain "/" or "..".
	// handleDelete turns name into file paths (name+".so", name+".web"),
	// so an unvalidated name is a path-traversal / arbitrary-delete vector
	// (e.g. DELETE /api/modules/..%2f..%2fetc). Enforce the same character
	// set the plugin upload path uses (ValidateModuleName / moduleNameRe).
	if err := ValidateModuleName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	var mod ModuleConfig
	if err := json.NewDecoder(r.Body).Decode(&mod); err != nil {
		writeJSONError(w, http.StatusBadRequest, "リクエストボディのパースに失敗しました: "+err.Error())
		return
	}

	// 一部の設定フィールドは保護/復旧機能またはコード実行境界（plugin_path）に
	// 触れる。パスベースのミドルウェア規則では設定編集とそのような変更を区別
	// できないため、ここで確認する。admin 専用フィールドが保存値と異なれば admin が
	// 必要だ。nil の checker は「admin でない」（fail-closed）として扱う。
	// Some config fields touch a protection/recovery capability or a
	// code-execution boundary (plugin_path). The path-based middleware rule
	// cannot tell a settings edit from such a change, so the check is done
	// here: if any admin-only field differs from the stored value, admin is
	// required. A nil checker is treated as "not admin" (fail-closed).
	if cur := h.storage.Get(name); cur != nil {
		// モジュールを無効化（または有効化）するとその監視が停止/開始するため、
		// 保護機能の変更だ。admin のみ。
		// Disabling (or enabling) a module starts/stops its monitoring, so it
		// is a protection capability change: admin only.
		if mod.Enabled != cur.Enabled {
			if h.adminChecker == nil || !h.adminChecker(r) {
				writeJSONError(w, http.StatusForbidden, "モジュールの有効/無効の変更には管理者権限が必要です")
				return
			}
		}
		// Type は agent がエントリをロードするかどうかを決めるものだ
		// （type="plugin" のみロードされ、他はスキップされる）。したがって
		// operator が "plugin" から別の値へ変えると、無効化と全く同じように
		// モジュールが黙って停止する。同じ保護ダウングレードだ。同じように
		// ゲートする。省略/空の type は保存値を保つ（ModulesStorage.Update 参照）
		// ため、これは実際の変更でのみ発火する。
		// Type is what the agent uses to decide whether to load the entry at
		// all (only type="plugin" is loaded; anything else is skipped). So an
		// operator changing it from "plugin" to another value silently stops
		// the module exactly like disabling it, which is the same protection
		// downgrade. Gate it the same way. An omitted/empty type keeps the
		// stored one (see ModulesStorage.Update), so this only fires on a real
		// change.
		if mod.Type != "" && mod.Type != cur.Type {
			if h.adminChecker == nil || !h.adminChecker(r) {
				writeJSONError(w, http.StatusForbidden, "モジュール種別の変更には管理者権限が必要です")
				return
			}
		}
		if field := changedAdminOnlyField(cur.Config, mod.Config); field != "" {
			if h.adminChecker == nil || !h.adminChecker(r) {
				writeJSONError(w, http.StatusForbidden, "設定「"+field+"」の変更には管理者権限が必要です")
				return
			}
		}
	}

	// Update は内部で永続化し、保存失敗時にロールバックする。
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

// handleDelete はモジュールを削除する。
// handleDelete deletes a module.
func (h *ModuleHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// 下でファイルパスを構築する前に name を検証する。URL エンコードされた
	// スラッシュ（%2f）は Go の単一セグメント {name} マッチを生き残り、値の中で
	// "/" になる。そのためこの確認が無いと "../../etc" のような名前が
	// filepath.Join(pluginsDir, name+".web") を pluginsDir の外へ脱出させ、
	// os.RemoveAll が plugins ディレクトリの外を削除してしまう。
	// ValidateModuleName は [A-Za-z0-9_-] 以外を拒否する。
	// Validate before the name is used to build file paths below. A
	// URL-encoded slash (%2f) survives Go's single-segment {name} match and
	// becomes "/" in the value, so without this check a name such as
	// "../../etc" would make filepath.Join(pluginsDir, name+".web") escape
	// pluginsDir and os.RemoveAll would delete outside the plugins
	// directory. ValidateModuleName rejects anything but [A-Za-z0-9_-].
	if err := ValidateModuleName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 削除前にモジュールの plugin path を捕捉し、その .so も除去できるようにする。
	// modules.json からエントリが消えたのに .so がディスクに残ると危険だ。
	// 同名の後日のアップロードが重複チェックを通過し、インストール済みプラグインを
	// 上書きしてしまう。
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

	// Delete は内部で永続化し、保存失敗時にロールバックする。
	// Delete persists internally and rolls back on save failure.
	if err := h.storage.Delete(name); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrModuleNotFound) {
			status = http.StatusNotFound
		}
		writeJSONError(w, status, err.Error())
		return
	}

	// 実行可能な成果物を除去する。pluginsDir 内のパスのみを触るため、細工された
	// plugin_path が任意のファイルを削除することはできない。
	// Remove the runnable artifacts. Only paths inside pluginsDir are
	// touched, so a crafted plugin_path cannot delete an arbitrary file.
	if h.pluginsDir != "" {
		candidates := []string{}
		if pluginPath != "" {
			candidates = append(candidates, pluginPath)
		}
		candidates = append(candidates,
			filepath.Join(h.pluginsDir, name+".so"),
			// A-3: 分離署名を .so と一緒に除去する。<name>.so.sig を残すと、
			// 同名で別の .so を後日アップロードしたとき検証が失敗し
			// （古い署名が一致しない）、プラグインが黙ってロードを拒否する。
			// A-3: remove the detached signature with the .so. Leaving
			// <name>.so.sig behind would make a later upload of a different
			// .so under the same name fail verification (the stale signature
			// does not match), so the plugin would silently refuse to load.
			pluginsig.SigPath(filepath.Join(h.pluginsDir, name+".so")),
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

// handleReload は設定を再読み込みする（ホットリロード）。
// handleReload reloads the config (hot reload).
func (h *ModuleHandler) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := h.storage.Load(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "再読み込みに失敗しました: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// ---------- ヘルパー ----------
// ---------- Helpers ----------

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	// セッション単位の API 応答（モジュール設定、プラグインメタデータ、ログ/アラート
	// データ）を共有プロキシやブラウザキャッシュから締め出す。
	// Keep session-scoped API responses (module configs, plugin metadata,
	// log/alert data) out of shared proxy and browser caches.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
