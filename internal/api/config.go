package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/configsnap"
	"Kizuna-Eye/pkg/fsutil"
)

// ConfigHandler は設定ファイル API を提供する。
// ConfigHandler serves config file APIs.
type ConfigHandler struct {
	agentConfigPath     string
	dashboardConfigPath string
	modulesConfigPath   string
	// templateDir は *.example.json テンプレートを読むディレクトリだ。既定は
	// dashboard 設定のディレクトリで、テンプレート読み込みがプロセスの作業
	// ディレクトリに依存しないようにする（別の場所の -config パスや systemd では
	// そうしないと 404 になる）。
	// templateDir is the directory the *.example.json templates are read
	// from. It defaults to the dashboard config's directory so template
	// loading does not depend on the process working directory (a -config
	// path elsewhere, or systemd, would otherwise 404).
	templateDir string
}

// NewConfigHandler は ConfigHandler を作る。
// NewConfigHandler creates a ConfigHandler.
func NewConfigHandler(agentConfigPath, dashboardConfigPath, modulesConfigPath string) *ConfigHandler {
	return &ConfigHandler{
		agentConfigPath:     agentConfigPath,
		dashboardConfigPath: dashboardConfigPath,
		modulesConfigPath:   modulesConfigPath,
		templateDir:         filepath.Dir(dashboardConfigPath),
	}
}

// RegisterRoutes は設定のルートを登録する。
// RegisterRoutes registers the config routes.
func (c *ConfigHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/config", c.handleGetConfig)
	mux.HandleFunc("PUT /api/config", c.handleUpdateConfig)
	mux.HandleFunc("GET /api/config/{type}/template", c.handleGetTemplate)
	// タイムトラベル: スナップショットの一覧と 1 件の復元。下の素の {type} ルートより
	// 具体的なので、mux が区別してルーティングする。
	// Time-travel: list snapshots and restore one. More specific than the
	// bare {type} routes below, so mux routes them distinctly.
	mux.HandleFunc("GET /api/config/{type}/snapshots", c.handleListSnapshots)
	mux.HandleFunc("POST /api/config/{type}/restore", c.handleRestoreSnapshot)
	mux.HandleFunc("GET /api/config/{type}", c.handleGetConfigByType)
	mux.HandleFunc("PUT /api/config/{type}", c.handleUpdateConfigByType)
}

// configPathFor は設定タイプをファイルパスへ解決する。未知のタイプでは ok が false になる。
// configPathFor resolves a config type to its file path. ok is false for an
// unknown type.
func (c *ConfigHandler) configPathFor(configType string) (string, bool) {
	switch configType {
	case "agent":
		return c.agentConfigPath, true
	case "dashboard":
		return c.dashboardConfigPath, true
	case "modules":
		return c.modulesConfigPath, true
	default:
		return "", false
	}
}

// handleListSnapshots は設定タイプの利用可能なスナップショットを新しい順で返す。
// handleListSnapshots returns the available snapshots for a config type,
// newest-first.
func (c *ConfigHandler) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	path, ok := c.configPathFor(r.PathValue("type"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid config type. Use 'agent', 'dashboard', or 'modules'")
		return
	}
	snaps, err := configsnap.List(path)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "スナップショット一覧の取得に失敗: "+err.Error())
		return
	}
	if snaps == nil {
		snaps = []configsnap.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"snapshots": snaps})
}

// handleRestoreSnapshot は設定タイプを指定名のスナップショットから復元する。
// スナップショット名はリクエスト本文（"name"）から来て、configsnap パッケージが
// 検証する（パス区切り不可、スナップショットディレクトリに存在すること）。
// handleRestoreSnapshot restores a config type from a named snapshot. The
// snapshot name comes from the request body ("name") and is validated by the
// configsnap package (no path separators, must exist in the snapshot dir).
func (c *ConfigHandler) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	path, ok := c.configPathFor(r.PathValue("type"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid config type. Use 'agent', 'dashboard', or 'modules'")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "リクエストの形式が不正です")
		return
	}
	if body.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "スナップショット名が指定されていません")
		return
	}
	if err := configsnap.Restore(path, body.Name); err != nil {
		writeJSONError(w, http.StatusBadRequest, "復元に失敗しました: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored", "name": body.Name})
}

// maskedValue は秘密の代わりに返すプレースホルダだ。
// maskedValue is the placeholder returned in place of a secret.
const maskedValue = "***"

// secretKeys は値が決してブラウザへ返されてはならない JSON キーだ。設定エディタは
// それらをマスクして表示し、保存時に maskedValue を送り返せば既存の秘密を
// 上書きせずに保持できる。
// secretKeys are JSON keys whose values must never be returned to the
// browser. The config editor shows them masked; sending maskedValue back on
// save keeps the existing secret instead of overwriting it.
var secretKeys = map[string]bool{
	"smtp_password": true,
	"smtp_username": true,
	"bot_token":     true,
	"token":         true, // LINE トークンと agent トークン / LINE token and agent token
	"agent_token":   true,
	"webhook_url":   true,
}

// maskSecrets は v のディープコピーを返し、すべての秘密値を maskedValue へ置き換える。
// ネストしたブロック（notifications、auth）を網羅するため、map と slice を走査する。
// maskSecrets returns a deep copy of v with every secret value replaced by
// maskedValue. Maps and slices are walked so nested blocks (notifications,
// auth) are covered.
func maskSecrets(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if secretKeys[k] {
				if s, ok := val.(string); ok && s != "" {
					out[k] = maskedValue
					continue
				}
			}
			out[k] = maskSecrets(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = maskSecrets(val)
		}
		return out
	default:
		return v
	}
}

// restoreSecrets は incoming の maskedValue を old の既存の秘密で置き換え、
// 保存時に送り返されたマスク済みフィールドが秘密を消さないようにする。
// restoreSecrets replaces maskedValue in incoming with the existing secret
// from old, so a masked field sent back on save does not wipe the secret.
func restoreSecrets(incoming, old interface{}) interface{} {
	switch t := incoming.(type) {
	case map[string]interface{}:
		oldMap, _ := old.(map[string]interface{})
		for k, val := range t {
			if secretKeys[k] {
				if s, ok := val.(string); ok && s == maskedValue {
					if oldMap != nil {
						if prev, ok := oldMap[k]; ok {
							t[k] = prev
							continue
						}
					}
					// 以前の値が無い: プレースホルダを落とし、保存されないようにする。
					// No previous value: drop the placeholder so it is not saved.
					t[k] = ""
					continue
				}
			}
			var oldVal interface{}
			if oldMap != nil {
				oldVal = oldMap[k]
			}
			t[k] = restoreSecrets(val, oldVal)
		}
		return t
	case []interface{}:
		oldArr, _ := old.([]interface{})
		// typeSeen は各タイプの要素がこれまで何個マッチしたかを数え、N 番目の
		// incoming 要素が N 番目の old 要素へ対応するようにする。タイプだけで
		// マッチすると、あるタイプのすべてのマスク済み要素がそのタイプの最初の
		// old 要素へ送られ、2 つの discord チャンネルがあると 2 番目の
		// webhook_url が 1 番目のもので上書きされてしまう。
		// typeSeen counts how many elements of each type have been matched so
		// far, so the Nth incoming element of a type maps to the Nth old one.
		// Matching by type alone would send every masked element of a type to
		// the first old element of that type, so with two discord channels the
		// second one's webhook_url would be overwritten by the first's.
		typeSeen := make(map[string]int)
		for i, val := range t {
			oldVal := matchOldElement(val, oldArr, i, typeSeen)
			t[i] = restoreSecrets(val, oldVal)
		}
		return t
	default:
		return incoming
	}
}

// matchOldElement は incoming 要素 val に対応する old 配列要素を見つける。
// "type" フィールドを持つオブジェクト（例: 通知チャンネル）は、同じタイプの
// N 番目の old 要素へマッチする（typeSeen が各タイプの既マッチ数を追跡する）。
// これにより並べ替え・削除で秘密が誤割り当てされず、同タイプの複数チャンネルが
// 各自の秘密を保つ。type を持たない要素は位置マッチへフォールバックする。
// matchOldElement finds the old array element that corresponds to incoming
// element val. Objects with a "type" field (e.g. notification channels) are
// matched to the Nth old element of the same type (typeSeen tracks how many of
// each type have already been matched), so a reorder/delete does not misassign
// secrets and multiple channels of the same type keep their own secret.
// Elements without a type fall back to positional matching.
func matchOldElement(val interface{}, oldArr []interface{}, idx int, typeSeen map[string]int) interface{} {
	vm, vok := val.(map[string]interface{})
	if vok {
		if vt, ok := vm["type"].(string); ok {
			want := typeSeen[vt]
			typeSeen[vt] = want + 1
			seen := 0
			for _, cand := range oldArr {
				cm, cok := cand.(map[string]interface{})
				if !cok {
					continue
				}
				if ct, ok := cm["type"].(string); ok && ct == vt {
					if seen == want {
						return cand
					}
					seen++
				}
			}
			return nil // このタイプの一致する old 要素が無い: マスクを落とす / no matching old element of this type: drop the mask
		}
	}
	if idx < len(oldArr) {
		return oldArr[idx]
	}
	return nil
}

// restoreSecretsFromFile はディスク上の設定を読み、incoming 値が maskedValue の
// 秘密を復元する。これにより設定エディタが送ったマスク済みプレースホルダが
// 実秘密を上書きしない。
// restoreSecretsFromFile loads the on-disk config and restores any secret
// whose incoming value is maskedValue, so masked placeholders sent by the
// config editor do not overwrite real secrets.
func (c *ConfigHandler) restoreSecretsFromFile(filePath string, incoming interface{}) interface{} {
	existing, err := c.loadJSONFile(filePath)
	if err != nil {
		return incoming
	}
	incoming = restoreSecrets(incoming, existing)
	// PUT はファイル全体を置き換えるため、セキュリティ上重要なブロックを省いた本文は
	// それを黙って削除してしまう。admin セッション（またはスクリプト化されたクライアント）が
	// 認証や agent トークンを無効化し、次の再起動でそれが効いてしまう容易な経路だ。
	// incoming 本文が言及しない場合、それらのブロックを既存ファイルから保持する。
	// A PUT replaces the whole file, so a body that omits a security-critical
	// block would silently delete it. That is an easy way for an admin session
	// (or a scripted client) to disable authentication or the agent token and
	// then have it take effect on the next restart. Preserve those blocks from
	// the existing file when the incoming body does not mention them.
	return preserveCriticalBlocks(incoming, existing)
}

// criticalBlocks は部分更新で落とされてはならないトップレベルの設定キーだ。
// incoming オブジェクトがキーを全く含まない場合、既存値がコピーされ、ブロックが
// 黙って削除されないようにする。
//
// "auth" / "notifications" は認証・通知（無効化されると防御が落ちる）。
// "plugins" は署名検証ポリシー（require_signature / public_key_file /
// inspect_isolation）。これが消えると未署名 .so が再びロード可能になり
// セキュリティが後退する。
// "plugins_upload_enabled" / "alert_history_key_path" は単独キーだが、
// 前者は RCE 境界（アップロード可否）、後者はアラート履歴の HMAC 署名鍵の
// 指定であり、欠落すると署名が無効化される。いずれも部分更新で消しては
// ならないため保護対象に含める。
// criticalBlocks are top-level config keys that must not be dropped by a
// partial update. If the incoming object does not contain the key at all, the
// existing value is copied over so the block is never silently removed.
//
// "auth" / "notifications" are auth/notifications (disabling them drops the
// defenses). "plugins" is the signature-verification policy
// (require_signature / public_key_file / inspect_isolation); losing it makes
// unsigned .so files loadable again, a security regression.
// "plugins_upload_enabled" / "alert_history_key_path" are single keys, but the
// former is an RCE boundary (upload on/off) and the latter names the alert
// history HMAC key, whose absence disables signing. Both must not be dropped
// by a partial update, so they are protected too.
var criticalBlocks = []string{
	"auth",
	"notifications",
	"plugins",
	"plugins_upload_enabled",
	"alert_history_key_path",
}

func preserveCriticalBlocks(incoming, existing interface{}) interface{} {
	inc, ok := incoming.(map[string]interface{})
	if !ok {
		return incoming
	}
	old, ok := existing.(map[string]interface{})
	if !ok {
		return incoming
	}
	for _, k := range criticalBlocks {
		if _, present := inc[k]; present {
			continue
		}
		if v, ok := old[k]; ok {
			inc[k] = v
		}
	}
	return inc
}

// exampleFileFor は設定タイプの example ファイル名を返す。
// exampleFileFor returns the example file name for a config type.
func exampleFileFor(configType string) (string, bool) {
	switch configType {
	case "agent":
		return "agent_config.example.json", true
	case "dashboard":
		return "dashboard_config.example.json", true
	case "modules":
		return "modules.json.example", true
	default:
		return "", false
	}
}

// handleGetTemplate は example テンプレートファイルをそのまま返す。
// handleGetTemplate returns the example template file as-is.
func (c *ConfigHandler) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	configType := r.PathValue("type")
	name, ok := exampleFileFor(configType)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid config type. Use 'agent', 'dashboard', or 'modules'")
		return
	}

	// 設定ディレクトリを優先し、次に素の名前（プロセスの作業ディレクトリ）、
	// 最後に実行ファイルのディレクトリへフォールバックする。これにより、example
	// ファイルが設定の隣に同梱されても、配備レイアウトでバイナリの隣にあっても
	// テンプレートが機能する。
	// Prefer the config directory, then fall back to the bare name (process
	// working directory) and finally the directory of the running executable,
	// so the template works whether the example files ship next to the config
	// or next to the binary in a deployed layout.
	candidates := []string{name}
	if c.templateDir != "" && c.templateDir != "." {
		candidates = append([]string{filepath.Join(c.templateDir, name)}, candidates...)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), name))
	}

	var data []byte
	var err error
	for _, cand := range candidates {
		data, err = os.ReadFile(cand)
		if err == nil {
			break
		}
	}
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Template not found: %s", name))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// handleGetConfig は全設定を返す。
// handleGetConfig returns all configs.
func (c *ConfigHandler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	agentCfg, err := c.loadJSONFile(c.agentConfigPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to load agent config: %v", err))
		return
	}

	dashboardCfg, err := c.loadJSONFile(c.dashboardConfigPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to load dashboard config: %v", err))
		return
	}

	modulesCfg, err := c.loadJSONFile(c.modulesConfigPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to load modules config: %v", err))
		return
	}

	configs := map[string]interface{}{
		"agent":     maskSecrets(agentCfg),
		"dashboard": maskSecrets(dashboardCfg),
		"modules":   modulesCfg,
	}

	writeJSON(w, http.StatusOK, configs)
}

// handleGetConfigByType は指定タイプの設定を返す。
// handleGetConfigByType returns the config for a given type.
func (c *ConfigHandler) handleGetConfigByType(w http.ResponseWriter, r *http.Request) {
	configType := r.PathValue("type")

	var filePath string
	var configName string

	switch configType {
	case "agent":
		filePath = c.agentConfigPath
		configName = "agent"
	case "dashboard":
		filePath = c.dashboardConfigPath
		configName = "dashboard"
	case "modules":
		filePath = c.modulesConfigPath
		configName = "modules"
	default:
		writeJSONError(w, http.StatusBadRequest, "Invalid config type. Use 'agent', 'dashboard', or 'modules'")
		return
	}

	cfg, err := c.loadJSONFile(filePath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to load %s config: %v", configName, err))
		return
	}

	writeJSON(w, http.StatusOK, maskSecrets(cfg))
}

// handleUpdateConfig は全設定を更新する。
// handleUpdateConfig updates all configs.
func (c *ConfigHandler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var configs map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&configs); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// マスクされた秘密（"***"）を、検証の「前に」ディスク上の設定から復元する。
	// これにより検証される値が保存される値と正確に一致する。先に検証すると、
	// 秘密がマスクされたままの正当な保存を拒否していた。既存の Discord
	// webhook_url はエディタへ "***" として返され、URL チェックが
	// "http または https のみ許可されます:"（空スキーム）で失敗していた。
	// Restore masked secrets ("***") from the on-disk config BEFORE
	// validating, so the values that are validated are exactly the ones that
	// will be saved. Validating first rejected a legitimate save whenever a
	// secret was left masked: an existing Discord webhook_url is returned to
	// the editor as "***", and the URL check then failed with "http または
	// https のみ許可されます:" (empty scheme).
	for _, key := range []string{"agent", "dashboard"} {
		if cfg, ok := configs[key]; ok {
			switch key {
			case "agent":
				configs[key] = c.restoreSecretsFromFile(c.agentConfigPath, cfg)
			case "dashboard":
				configs[key] = c.restoreSecretsFromFile(c.dashboardConfigPath, cfg)
			}
		}
	}

	// 何かを書く前に存在する全設定を検証し、一括エンドポイントが
	// PUT /api/config/{type} と同じように振る舞い、必須フィールドの欠けた設定を
	// 永続化できないようにする。
	// Validate every present config before writing anything, so the bulk
	// endpoint behaves the same as PUT /api/config/{type} and cannot persist
	// a config that is missing required fields.
	for _, key := range []string{"agent", "dashboard", "modules"} {
		if cfg, ok := configs[key]; ok {
			if err := c.validateConfig(key, cfg); err != nil {
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Validation failed for %s: %v", key, err))
				return
			}
		}
	}

	if agentCfg, ok := configs["agent"]; ok {
		_ = configsnap.Snapshot(c.agentConfigPath)
		if err := c.saveJSONFile(c.agentConfigPath, agentCfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save agent config: %v", err))
			return
		}
	}

	if dashboardCfg, ok := configs["dashboard"]; ok {
		_ = configsnap.Snapshot(c.dashboardConfigPath)
		if err := c.saveJSONFile(c.dashboardConfigPath, dashboardCfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save dashboard config: %v", err))
			return
		}
	}

	if modulesCfg, ok := configs["modules"]; ok {
		_ = configsnap.Snapshot(c.modulesConfigPath)
		if err := c.saveJSONFile(c.modulesConfigPath, modulesCfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save modules config: %v", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleUpdateConfigByType は指定タイプの設定を更新する。
// handleUpdateConfigByType updates the config for a given type.
func (c *ConfigHandler) handleUpdateConfigByType(w http.ResponseWriter, r *http.Request) {
	configType := r.PathValue("type")

	var filePath string
	var configName string

	switch configType {
	case "agent":
		filePath = c.agentConfigPath
		configName = "agent"
	case "dashboard":
		filePath = c.dashboardConfigPath
		configName = "dashboard"
	case "modules":
		filePath = c.modulesConfigPath
		configName = "modules"
	default:
		writeJSONError(w, http.StatusBadRequest, "Invalid config type. Use 'agent', 'dashboard', or 'modules'")
		return
	}

	var cfg interface{}
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// 検証の前にマスクされた秘密を復元し、検証済みの値が保存される値と一致する
	// ようにする（handleUpdateConfig 参照）。modules.json は秘密を持たないので
	// スキップする（そのトップレベルはオブジェクトではなく配列だ）。
	// Restore masked secrets before validating, so the validated values match
	// what is saved (see handleUpdateConfig). modules.json holds no secrets, so
	// it is skipped (its top level is an array, not an object).
	if configType != "modules" {
		cfg = c.restoreSecretsFromFile(filePath, cfg)
	}

	if err := c.validateConfig(configType, cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Validation failed: "+err.Error())
		return
	}

	_ = configsnap.Snapshot(filePath)
	if err := c.saveJSONFile(filePath, cfg); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save %s config: %v", configName, err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "type": configName})
}

// loadJSONFile は JSON ファイルを読む。
// loadJSONFile reads a JSON file.
func (c *ConfigHandler) loadJSONFile(filePath string) (interface{}, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // ファイル欠落 -> nil / file missing -> nil
		}
		return nil, err
	}

	var result interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// saveJSONFile は JSON ファイルをアトミックに書く。
// 同一ディレクトリの一時ファイルへ書き、それを rename するため、クラッシュや
// 並行読み取りが部分的に書かれた設定を観測することが決してない。
// saveJSONFile writes a JSON file atomically.
// It writes to a temp file in the same directory and renames it into place,
// so a crash or concurrent reader never observes a partially written config.
func (c *ConfigHandler) saveJSONFile(filePath string, data interface{}) error {
	// ディレクトリの存在を保証する。
	// Ensure the directory exists.
	dir := filepath.Dir(filePath)
	// 設定ファイルは秘密を含む。ディレクトリも所有者のみに保つ。
	// Config files hold secrets; keep the directory owner-only too.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	jsonData = append(jsonData, '\n')

	// 設定ファイルは秘密（agent_token、smtp_password、webhook_url）を含み得る。
	// 既定は 0600 とし、ファイルが既に存在する場合のみ「既存の」モードを保つ。
	// これにより新規作成の設定が world-readable になることが決してない。
	// Config files may contain secrets (agent_token, smtp_password,
	// webhook_url). Default to 0600 and only preserve an *existing* mode when
	// the file already exists, so a freshly created config is never
	// world-readable.
	mode := os.FileMode(0600)
	if info, statErr := os.Stat(filePath); statErr == nil {
		mode = info.Mode().Perm()
	}

	// fsutil.WriteFileAtomic: シンボリックリンクを拒否し、mode を一時ファイル
	// に適用してから fsync + rename する（L-12: 同じ処理が10箇所にあった）。
	// fsutil.WriteFileAtomic: reject a symlink, apply the mode to the temp
	// file, then fsync + rename (L-12: the same logic existed in ten places).
	return fsutil.WriteFileAtomic(filePath, jsonData, mode)
}

// validatePluginsBlock は使用不能なプラグインセキュリティポリシーが永続化される前に
// 拒否する。nil / 非オブジェクト値は「plugins ブロック無し」を意味し、許容される
// （既定が適用される）。
//
// 規則はローダーが起動時に走らせる config.PluginSecurityConfig.Validate と対になる。
// require_signature=true で public_key_file が無いのは使用不能なので、保存すると
// agent（と dashboard）が起動を拒否する設定を残してしまう。ここで捕まえることで、
// 無言の遅延障害が即時の 400 になる。
// validatePluginsBlock rejects an unusable plugin-security policy before it is
// persisted. A nil / non-object value means "no plugins block" and is allowed
// (the defaults apply).
//
// The rule mirrors config.PluginSecurityConfig.Validate, which the loaders run
// at startup: require_signature=true without a public_key_file is unusable, so
// saving it would leave a config that the agent (and dashboard) refuse to start
// with. Catching it here turns a silent, delayed outage into an immediate 400.
func validatePluginsBlock(v interface{}) error {
	plugins, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	requireSig, _ := plugins["require_signature"].(bool)
	if !requireSig {
		return nil
	}
	pub, _ := plugins["public_key_file"].(string)
	if strings.TrimSpace(pub) == "" {
		return fmt.Errorf("plugins: require_signature=true には public_key_file が必須です（Ed25519 公開鍵のパス）")
	}
	return nil
}

// validateNotificationChannels は notifications ブロックを LoadDashboardConfig と
// 同じように検証し、API が再起動時に読み込み失敗する設定を拒否するようにする。
// nil / 非オブジェクト値は「通知無し」として扱い、許容する。
// validateNotificationChannels validates the notifications block the same way
// LoadDashboardConfig does, so the API rejects configs that would otherwise
// fail to load on restart. A nil / non-object value is treated as "no
// notifications" and is allowed.
func validateNotificationChannels(v interface{}) error {
	notif, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	enabled, _ := notif["enabled"].(bool)
	if !enabled {
		return nil
	}
	channels, _ := notif["channels"].([]interface{})
	for i, raw := range channels {
		ch, ok := raw.(map[string]interface{})
		if !ok {
			return fmt.Errorf("notifications.channels[%d]: オブジェクトである必要があります", i)
		}
		chEnabled, _ := ch["enabled"].(bool)
		if !chEnabled {
			continue
		}
		typ, _ := ch["type"].(string)
		// 大文字小文字・空白を許容し、notify.FromConfig の実行時ディスパッチと
		// 一致させる（例: "Discord" がここで拒否されてはならない）。
		// Accept any casing/whitespace so this matches the runtime dispatch
		// in notify.FromConfig (e.g. "Discord" must not be rejected here).
		switch strings.ToLower(strings.TrimSpace(typ)) {
		case "discord", "slack":
			wurl, _ := ch["webhook_url"].(string)
			if strings.TrimSpace(wurl) == "" {
				return fmt.Errorf("notifications.channels[%d]: %s には webhook_url が必須です", i, typ)
			}
			if err := config.ValidateWebhookURL(wurl); err != nil {
				return fmt.Errorf("notifications.channels[%d]: %s の webhook_url が不正です: %w", i, typ, err)
			}
		case "telegram":
			bot, _ := ch["bot_token"].(string)
			chat, _ := ch["chat_id"].(string)
			if strings.TrimSpace(bot) == "" || strings.TrimSpace(chat) == "" {
				return fmt.Errorf("notifications.channels[%d]: telegram には bot_token と chat_id が必須です", i)
			}
		case "line":
			if s, _ := ch["token"].(string); strings.TrimSpace(s) == "" {
				return fmt.Errorf("notifications.channels[%d]: line には token が必須です", i)
			}
		case "email":
			host, _ := ch["smtp_host"].(string)
			to, _ := ch["email_to"].(string)
			if strings.TrimSpace(host) == "" || strings.TrimSpace(to) == "" {
				return fmt.Errorf("notifications.channels[%d]: email には smtp_host と email_to が必須です", i)
			}
		default:
			return fmt.Errorf("notifications.channels[%d]: 不明な type です: %s", i, typ)
		}
	}
	return nil
}

// validateConfig は簡単な設定検証を行う。
// validateConfig performs simple config validation.
func (c *ConfigHandler) validateConfig(configType string, cfg interface{}) error {
	switch configType {
	case "agent":
		// agent 設定を LoadAgentConfig が使うのと同じ規則で検証し、再起動時に
		// 読み込み失敗する設定を今拒否する。
		// Validate agent config against the same rules LoadAgentConfig uses,
		// so a config that would fail to load on restart is rejected now.
		cfgMap, ok := cfg.(map[string]interface{})
		if !ok {
			return fmt.Errorf("agent config must be an object")
		}
		if dashboardURL, ok := cfgMap["dashboard_url"].(string); !ok || dashboardURL == "" {
			return fmt.Errorf("dashboard_url is required")
		} else if err := config.ValidateAgentURL(dashboardURL); err != nil {
			return fmt.Errorf("dashboard_url が不正です: %w", err)
		}
		if v, ok := cfgMap["interval"]; ok {
			f, isNum := v.(float64)
			if !isNum || f <= 0 {
				return fmt.Errorf("interval は 0 より大きい値を指定してください")
			}
		}
		if v, ok := cfgMap["disk_path"]; ok {
			s, isStr := v.(string)
			if !isStr || strings.TrimSpace(s) == "" {
				return fmt.Errorf("disk_path は空にできません")
			}
		}
		// plugins ブロックは未署名 .so をロードしてよいかを決める。ここで
		// 自己矛盾したポリシー（require_signature=true で public_key_file 無し）を
		// 保存すると受け入れられ、次回再起動で agent が起動を拒否する
		// （fail-closed）。保存時に拒否し、失敗を死んだサービスではなく明確な
		// 400 にする。
		// The plugins block decides whether an unsigned .so may load. Saving a
		// self-contradictory policy here (require_signature=true with no
		// public_key_file) would be accepted, and then the agent would refuse
		// to start on the next restart (fail-closed). Reject it at save time so
		// the failure is a clear 400 instead of a dead service.
		if err := validatePluginsBlock(cfgMap["plugins"]); err != nil {
			return err
		}
	case "dashboard":
		// dashboard 設定を LoadDashboardConfig が使うのと同じ規則で検証する。
		// 特に、有効な通知チャンネルに必須フィールドが欠けている（例: webhook_url の
		// 無い Discord チャンネル）と旧チェックを通過し、再起動時にサーバーを
		// クラッシュさせていた。
		// Validate dashboard config against the same rules LoadDashboardConfig
		// uses. In particular, an enabled notification channel missing its
		// required field (e.g. a Discord channel with no webhook_url) would
		// pass the old check but crash the server on restart.
		cfgMap, ok := cfg.(map[string]interface{})
		if !ok {
			return fmt.Errorf("dashboard config must be an object")
		}
		if listenAddr, ok := cfgMap["listen_addr"].(string); !ok || listenAddr == "" {
			return fmt.Errorf("listen_addr is required")
		}
		if err := validateNotificationChannels(cfgMap["notifications"]); err != nil {
			return err
		}
		// agent ブランチ参照: 使用不能な署名ポリシーを保存してはならない。
		// See the agent branch: an unusable signature policy must not be saved.
		if err := validatePluginsBlock(cfgMap["plugins"]); err != nil {
			return err
		}
		// agent トークンを保護する。auth 有効時に空の agent_token は従来の
		// ヒューリスティック agent 検出を強制し、?role=agent（Origin 無し）の
		// 任意の非ブラウザクライアントが agent へ昇格し、偽 status を注入できる。
		// 空にするのを拒否する。
		// Guard the agent token: when auth is enabled, an empty agent_token
		// forces the legacy heuristic agent detection, which lets any
		// non-browser client with ?role=agent (no Origin) be promoted to the
		// agent and inject fake statuses. Refuse to empty it.
		if authRaw, ok := cfgMap["auth"].(map[string]interface{}); ok {
			if enabled, _ := authRaw["enabled"].(bool); enabled {
				if tok, ok := authRaw["agent_token"].(string); ok && strings.TrimSpace(tok) == "" {
					return fmt.Errorf("auth.enabled=true のとき auth.agent_token を空にはできません（Agent 認証が無効化されます）")
				}
			}
		}
	case "modules":
		// modules 設定を検証する。名前が非空のオブジェクトの配列でなければならない。
		// Validate modules config: it must be an array of objects each with
		// a non-empty name.
		cfgArray, ok := cfg.([]interface{})
		if !ok {
			return fmt.Errorf("modules config must be an array")
		}
		for i, item := range cfgArray {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				return fmt.Errorf("module[%d]: must be an object", i)
			}
			name, ok := itemMap["name"].(string)
			if !ok || name == "" {
				return fmt.Errorf("module[%d]: name is required", i)
			}
		}
	}
	return nil
}
