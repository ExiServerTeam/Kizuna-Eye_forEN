package module

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// ============================================================
// Module は全モジュールが実装する基本インターフェース
// ============================================================
type Module interface {
	Name() string
	Interval() time.Duration
	Run(ctx context.Context) error
	Description() string
	Init(ctx context.Context) error
}

// ============================================================
// Logger はログ出力インターフェース
// ============================================================
type Logger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
	Debug(format string, args ...interface{})
}

// ============================================================
// ModuleManager はモジュールを管理する
// ============================================================
type ModuleManager interface {
	Register(ctx context.Context, module Module) error
	Get(name string) (Module, bool)
	Start(ctx context.Context)
	Stop()
}

type moduleManager struct {
	mu      sync.RWMutex
	modules map[string]Module
	logger  Logger
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func NewModuleManager(logger Logger) ModuleManager {
	return &moduleManager{
		modules: make(map[string]Module),
		logger:  logger,
	}
}

func (m *moduleManager) Register(ctx context.Context, module Module) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := module.Name()
	if _, exists := m.modules[name]; exists {
		return fmt.Errorf("module %s already registered", name)
	}
	if err := module.Init(ctx); err != nil {
		return err
	}
	m.modules[name] = module
	return nil
}

func (m *moduleManager) Get(name string) (Module, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mod, ok := m.modules[name]
	return mod, ok
}

func (m *moduleManager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.mu.Unlock()

	if m.logger != nil {
		m.logger.Info("ModuleManager started")
	}

	for _, mod := range m.modules {
		m.wg.Add(1)
		go m.runModuleLoop(ctx, mod)
	}
}

func (m *moduleManager) runModuleLoop(ctx context.Context, mod Module) {
	defer m.wg.Done()
	interval := mod.Interval()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	mod.Run(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mod.Run(ctx)
		}
	}
}

func (m *moduleManager) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()

	m.wg.Wait()
	if m.logger != nil {
		m.logger.Info("ModuleManager stopped")
	}
}

// ============================================================
// バックアップ関連の型定義
// プラグイン（.so）とエージェント本体で共有する
// ============================================================

// BackupStatus はバックアップモジュールの最新実行結果を表す
type BackupStatus struct {
	Name    string `json:"name,omitempty"`
	LastRun string `json:"last_run,omitempty"`
	Status  string `json:"status,omitempty"`
	Size    int64  `json:"size,omitempty"`
	NextRun string `json:"next_run,omitempty"`
}

// BackupStatusProvider はバックアップ対応プラグインが実装する
type BackupStatusProvider interface {
	GetBackupStatus() *BackupStatus
}

// ============================================================
// BackupRunner はバックアップ実行可能なプラグインが実装する
// ============================================================
type BackupRunner interface {
	RunBackup(ctx context.Context) error
}

// ============================================================
// BackupResult はバックアップ実行結果
// ============================================================
type BackupResult struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"`
	Size       int64     `json:"size"`
	Error      string    `json:"error,omitempty"`
}

// ============================================================
// Phase 8-6 追加: 設定フィールド自己申告機構
// プラグインが .so 側で「どんな設定項目が必要か」を自己申告する。
// フロントエンドはこの情報だけを使って動的フォームを組み立てる。
// ============================================================

// ConfigFieldType はフォームの入力種別や。
// マジックストリングを避けるために型を切る。
type ConfigFieldType string

const (
	FieldText     ConfigFieldType = "text"
	FieldNumber   ConfigFieldType = "number"
	FieldSelect   ConfigFieldType = "select"
	FieldCheckbox ConfigFieldType = "checkbox"
	FieldPassword ConfigFieldType = "password"
	FieldTextarea ConfigFieldType = "textarea"
)

// ConfigField はプラグインが自己申告する設定項目や。
// JSON タグはフロントエンドのフォーム生成でそのまま使う。
type ConfigField struct {
	Key         string          `json:"key"`
	Label       string          `json:"label"`
	Type        ConfigFieldType `json:"type"`
	Default     string          `json:"default,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Options     []string        `json:"options,omitempty"`
	Placeholder string          `json:"placeholder,omitempty"`
	Hint        string          `json:"hint,omitempty"`
	Group       string          `json:"group,omitempty"`
	Min         *float64        `json:"min,omitempty"`
	Max         *float64        `json:"max,omitempty"`
	Pattern     string          `json:"pattern,omitempty"`
}

// Validate は値がフィールド定義に合致するか検証する。
// サーバーサイドバリデーションの要や。フロント任せにしたらあかん。
func (f *ConfigField) Validate(raw string) error {
	if f.Required && raw == "" {
		return fmt.Errorf("%s は必須です", f.Label)
	}
	if raw == "" {
		return nil
	}

	switch f.Type {
	case FieldNumber:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("%s は数値で入力してください", f.Label)
		}
		if f.Min != nil && v < *f.Min {
			return fmt.Errorf("%s は %.2f 以上で入力してください", f.Label, *f.Min)
		}
		if f.Max != nil && v > *f.Max {
			return fmt.Errorf("%s は %.2f 以下で入力してください", f.Label, *f.Max)
		}
	case FieldSelect:
		if len(f.Options) > 0 {
			ok := false
			for _, o := range f.Options {
				if o == raw {
					ok = true
					break
				}
			}
			if !ok {
				return fmt.Errorf("%s は選択肢の中から選んでください", f.Label)
			}
		}
	case FieldCheckbox:
		if raw != "true" && raw != "false" {
			return fmt.Errorf("%s は true/false で入力してください", f.Label)
		}
	}

	if f.Pattern != "" {
		re, err := regexp.Compile(f.Pattern)
		if err != nil {
			return fmt.Errorf("%s のパターン定義が不正です: %w", f.Label, err)
		}
		if !re.MatchString(raw) {
			return fmt.Errorf("%s の形式が正しくありません", f.Label)
		}
	}
	return nil
}

// ConfigProvider はプラグインが設定項目を自己申告するためのインターフェースや。
// 実装は任意。実装しとるプラグインだけが動的フォームを使える。
type ConfigProvider interface {
	GetConfigFields() []ConfigField
}

// ValidateAll はフィールド定義に沿って全設定を一括検証する。
func ValidateAll(fields []ConfigField, values map[string]string) error {
	for i := range fields {
		f := &fields[i]
		if err := f.Validate(values[f.Key]); err != nil {
			return err
		}
	}
	return nil
}

// ApplyDefaults は未入力のフィールドにデフォルト値を埋める。
func ApplyDefaults(fields []ConfigField, values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	for i := range fields {
		f := &fields[i]
		if _, ok := out[f.Key]; !ok || out[f.Key] == "" {
			if f.Default != "" {
				out[f.Key] = f.Default
			}
		}
	}
	return out
}

// ============================================================
// Phase 8-6 追加: 表示名自己申告機構
// プラグインが「バッジに表示する短い名前」を自己申告するための
// オプショナルインターフェースや。実装は任意。
// ============================================================

// DisplayNameProvider はプラグインが表示用の短い名前を申告する。
// 実装しとるプラグインは、フロントのバッジにこの名前が表示される。
// 未実装のプラグインは、フロント側のフォールバック（PLUGIN表示）になる。
type DisplayNameProvider interface {
	DisplayName() string
}
