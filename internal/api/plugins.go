package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"Kizuna-Eye/pkg/module"
)

// PluginManager はプラグインを管理する
type PluginManager struct {
	storage    *ModulesStorage
	logger     module.Logger
	pluginsDir string
	inspectBin string
}

// PluginMeta は plugins/{name}.meta.json の中身や。
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
}

// NewPluginManager は新しいプラグインマネージャーを作成する。
func NewPluginManager(storage *ModulesStorage, logger module.Logger) (*PluginManager, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("実行ファイルパス取得失敗: %w", err)
	}
	baseDir := filepath.Dir(exePath)

	pluginsDir := filepath.Join(baseDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return nil, fmt.Errorf("plugins ディレクトリ作成失敗: %w", err)
	}

	inspectBin := filepath.Join(baseDir, "plugin-inspect")
	if _, err := os.Stat(inspectBin); err != nil {
		return nil, fmt.Errorf("plugin-inspect が見つかりません: %s (%w)", inspectBin, err)
	}

	return &PluginManager{
		storage:    storage,
		logger:     logger,
		pluginsDir: pluginsDir,
		inspectBin: inspectBin,
	}, nil
}

// RegisterRoutes はプラグイン関連のルートを登録する
func (p *PluginManager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/plugins/upload", p.handleUpload)
	mux.HandleFunc("GET /api/plugins", p.handleList)
	mux.HandleFunc("DELETE /api/plugins/{name}", p.handleDelete)
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

// handleUpload はプラグイン .so ファイルをアップロードし、
// plugin-inspect でメタ情報を抽出して plugins/ に保存する。
func (p *PluginManager) handleUpload(w http.ResponseWriter, r *http.Request) {
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
	tmpPath := soPath + ".tmp"

	out, err := os.Create(tmpPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "一時ファイル作成失敗: "+err.Error())
		return
	}
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

	// plugin-inspect で検証
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

	// meta.json 保存
	metaPath := filepath.Join(p.pluginsDir, name+".meta.json")
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "meta.json 書き込み失敗: "+err.Error())
		return
	}

	// modules.json に登録
	modConfig := ModuleConfig{
		Name:    name,
		Type:    "plugin",
		Enabled: true,
		Config:  json.RawMessage(`{"plugin_path":"` + soPath + `"}`),
	}
	if err := p.storage.Add(modConfig); err != nil {
		if p.logger != nil {
			p.logger.Warn("モジュール登録スキップ: %s: %v", name, err)
		}
	} else {
		if err := p.storage.Save(); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "設定保存失敗: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, meta)
}

// handleList は登録済みプラグイン一覧を返す。
func (p *PluginManager) handleList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(p.pluginsDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "plugins ディレクトリ読み込み失敗: "+err.Error())
		return
	}

	list := make([]*PluginMeta, 0, len(entries))
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
		list = append(list, &meta)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"plugins": list,
		"dir":     p.pluginsDir,
	})
}

// handleDelete はプラグインを削除する
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

	if err := p.storage.Delete(name); err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := p.storage.Save(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "保存失敗: "+err.Error())
		return
	}

	_ = os.Remove(filepath.Join(p.pluginsDir, name+".so"))
	_ = os.Remove(filepath.Join(p.pluginsDir, name+".meta.json"))

	if p.logger != nil {
		p.logger.Info("プラグインモジュール削除: %s", name)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name})
}

// inspectPlugin は plugin-inspect を実行して結果をパースする。
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
