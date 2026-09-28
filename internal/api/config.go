package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ConfigHandler serves config file APIs.
type ConfigHandler struct {
	agentConfigPath     string
	dashboardConfigPath string
	modulesConfigPath   string
}

// NewConfigHandler creates a ConfigHandler.
func NewConfigHandler(agentConfigPath, dashboardConfigPath, modulesConfigPath string) *ConfigHandler {
	return &ConfigHandler{
		agentConfigPath:     agentConfigPath,
		dashboardConfigPath: dashboardConfigPath,
		modulesConfigPath:   modulesConfigPath,
	}
}

// RegisterRoutes registers the config routes.
func (c *ConfigHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/config", c.handleGetConfig)
	mux.HandleFunc("PUT /api/config", c.handleUpdateConfig)
	mux.HandleFunc("GET /api/config/{type}/template", c.handleGetTemplate)
	mux.HandleFunc("GET /api/config/{type}", c.handleGetConfigByType)
	mux.HandleFunc("PUT /api/config/{type}", c.handleUpdateConfigByType)
}

// maskedValue is the placeholder returned in place of a secret.
const maskedValue = "***"

// secretKeys are JSON keys whose values must never be returned to the
// browser. The config editor shows them masked; sending maskedValue back on
// save keeps the existing secret instead of overwriting it.
var secretKeys = map[string]bool{
	"smtp_password": true,
	"smtp_username": true,
	"bot_token":     true,
	"token":         true, // LINE token and agent token
	"agent_token":   true,
	"webhook_url":   true,
}

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
		for i, val := range t {
			var oldVal interface{}
			if i < len(oldArr) {
				oldVal = oldArr[i]
			}
			t[i] = restoreSecrets(val, oldVal)
		}
		return t
	default:
		return incoming
	}
}

// restoreSecretsFromFile loads the on-disk config and restores any secret
// whose incoming value is maskedValue, so masked placeholders sent by the
// config editor do not overwrite real secrets.
func (c *ConfigHandler) restoreSecretsFromFile(filePath string, incoming interface{}) interface{} {
	existing, err := c.loadJSONFile(filePath)
	if err != nil {
		return incoming
	}
	return restoreSecrets(incoming, existing)
}

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

// handleGetTemplate returns the example template file as-is.
func (c *ConfigHandler) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	configType := r.PathValue("type")
	name, ok := exampleFileFor(configType)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid config type. Use 'agent', 'dashboard', or 'modules'")
		return
	}

	data, err := os.ReadFile(name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Template not found: %s", name))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

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

// handleUpdateConfig updates all configs.
func (c *ConfigHandler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var configs map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&configs); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

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
		if err := c.saveJSONFile(c.agentConfigPath, c.restoreSecretsFromFile(c.agentConfigPath, agentCfg)); err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save agent config: %v", err))
			return
		}
	}

	if dashboardCfg, ok := configs["dashboard"]; ok {
		if err := c.saveJSONFile(c.dashboardConfigPath, c.restoreSecretsFromFile(c.dashboardConfigPath, dashboardCfg)); err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save dashboard config: %v", err))
			return
		}
	}

	if modulesCfg, ok := configs["modules"]; ok {
		if err := c.saveJSONFile(c.modulesConfigPath, modulesCfg); err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save modules config: %v", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

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

	if err := c.validateConfig(configType, cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Validation failed: "+err.Error())
		return
	}

	cfg = c.restoreSecretsFromFile(filePath, cfg)
	if err := c.saveJSONFile(filePath, cfg); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to save %s config: %v", configName, err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "type": configName})
}

// loadJSONFile reads a JSON file.
func (c *ConfigHandler) loadJSONFile(filePath string) (interface{}, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // file missing -> nil
		}
		return nil, err
	}

	var result interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// saveJSONFile writes a JSON file atomically.
// It writes to a temp file in the same directory and renames it into place,
// so a crash or concurrent reader never observes a partially written config.
func (c *ConfigHandler) saveJSONFile(filePath string, data interface{}) error {
	// Ensure the directory exists.
	dir := filepath.Dir(filePath)
	// Config files hold secrets; keep the directory owner-only too.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	jsonData = append(jsonData, '\n')

	// Config files may contain secrets (agent_token, smtp_password,
	// webhook_url). Default to 0600 and only preserve an *existing* mode when
	// the file already exists, so a freshly created config is never
	// world-readable.
	mode := os.FileMode(0600)
	if info, statErr := os.Stat(filePath); statErr == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(filePath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup; a no-op after a successful rename.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(jsonData); err != nil {
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
	return os.Rename(tmpName, filePath)
}

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
		// Accept any casing/whitespace so this matches the runtime dispatch
		// in notify.FromConfig (e.g. "Discord" must not be rejected here).
		switch strings.ToLower(strings.TrimSpace(typ)) {
		case "discord", "slack":
			if s, _ := ch["webhook_url"].(string); strings.TrimSpace(s) == "" {
				return fmt.Errorf("notifications.channels[%d]: %s には webhook_url が必須です", i, typ)
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

// validateConfig performs simple config validation.
func (c *ConfigHandler) validateConfig(configType string, cfg interface{}) error {
	switch configType {
	case "agent":
		// Validate agent config against the same rules LoadAgentConfig uses,
		// so a config that would fail to load on restart is rejected now.
		cfgMap, ok := cfg.(map[string]interface{})
		if !ok {
			return fmt.Errorf("agent config must be an object")
		}
		if dashboardURL, ok := cfgMap["dashboard_url"].(string); !ok || dashboardURL == "" {
			return fmt.Errorf("dashboard_url is required")
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
	case "dashboard":
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
	case "modules":
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
