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
// Module is the base interface all modules implement.
// ============================================================
type Module interface {
	Name() string
	Interval() time.Duration
	Run(ctx context.Context) error
	Description() string
	Init(ctx context.Context) error
}

// ============================================================
// Logger is the logging interface.
// ============================================================
type Logger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
	Debug(format string, args ...interface{})
}

// ============================================================
// ModuleManager manages modules.
// ============================================================
type ModuleManager interface {
	Register(ctx context.Context, module Module) error
	Get(name string) (Module, bool)
	Start(ctx context.Context)
	Stop()
	// NotifyConfigChanged asks the module's run loop to re-read its
	// interval, so a config change takes effect without a restart.
	NotifyConfigChanged(name string)
}

type moduleManager struct {
	mu      sync.RWMutex
	modules map[string]Module
	resets  map[string]chan struct{} // module name -> interval reset signal
	logger  Logger
	running bool
	cancel  context.CancelFunc
	runCtx  context.Context
	wg      sync.WaitGroup
}

func NewModuleManager(logger Logger) ModuleManager {
	return &moduleManager{
		modules: make(map[string]Module),
		resets:  make(map[string]chan struct{}),
		logger:  logger,
	}
}

func (m *moduleManager) Register(ctx context.Context, module Module) error {
	m.mu.Lock()
	name := module.Name()
	if _, exists := m.modules[name]; exists {
		m.mu.Unlock()
		return fmt.Errorf("module %s already registered", name)
	}
	if err := module.Init(ctx); err != nil {
		m.mu.Unlock()
		return err
	}
	m.modules[name] = module
	if m.resets[name] == nil {
		m.resets[name] = make(chan struct{}, 1)
	}
	// If the manager is already running, start this module's loop immediately.
	// Modules registered after Start() (e.g. hot-reloaded plugins) would
	// otherwise never run.
	running := m.running
	runCtx := m.runCtx
	m.mu.Unlock()

	if running && runCtx != nil {
		m.wg.Add(1)
		go m.runModuleLoop(runCtx, module)
	}
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
	m.runCtx = ctx
	// Snapshot modules under lock to avoid a race with concurrent Register.
	mods := make([]Module, 0, len(m.modules))
	for _, mod := range m.modules {
		mods = append(mods, mod)
	}
	m.mu.Unlock()

	if m.logger != nil {
		m.logger.Info("ModuleManager started")
	}

	for _, mod := range mods {
		m.wg.Add(1)
		go m.runModuleLoop(ctx, mod)
	}
}

func (m *moduleManager) runModuleLoop(ctx context.Context, mod Module) {
	defer m.wg.Done()
	if mod.Interval() <= 0 {
		return
	}

	m.mu.RLock()
	resetCh := m.resets[mod.Name()]
	m.mu.RUnlock()

	mod.Run(ctx)

	for {
		// Re-read the interval every cycle so a config change takes effect.
		interval := mod.Interval()
		if interval <= 0 {
			return
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-resetCh:
			// Interval changed; re-evaluate without running now.
			timer.Stop()
		case <-timer.C:
			mod.Run(ctx)
		}
	}
}

// NotifyConfigChanged wakes a module's run loop so it re-reads its interval.
func (m *moduleManager) NotifyConfigChanged(name string) {
	m.mu.RLock()
	ch := m.resets[name]
	m.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
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
// Backup types shared between plugins (.so) and the agent.
// ============================================================

// BackupStatus is the latest backup run result.
type BackupStatus struct {
	Name    string `json:"name,omitempty"`
	LastRun string `json:"last_run,omitempty"`
	Status  string `json:"status,omitempty"`
	Size    int64  `json:"size,omitempty"`
	NextRun string `json:"next_run,omitempty"`
}

// BackupStatusProvider is implemented by backup plugins.
type BackupStatusProvider interface {
	GetBackupStatus() *BackupStatus
}

// ============================================================
// BackupRunner is implemented by plugins that can run backups.
// ============================================================
type BackupRunner interface {
	RunBackup(ctx context.Context) error
}

// ============================================================
// Security types shared between security plugins (.so) and the agent.
// ============================================================

// SecurityEvent is one security-relevant event detected by a plugin.
type SecurityEvent struct {
	Plugin    string    `json:"plugin"`
	Category  string    `json:"category"` // ssh_login / ssh_failed / sudo / install / account / config
	Level     string    `json:"level"`    // critical / warning / info
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Source    string    `json:"source,omitempty"` // log file the event came from
	Actor     string    `json:"actor,omitempty"`  // user who performed the action
	IP        string    `json:"ip,omitempty"`     // source IP address
	Timestamp time.Time `json:"timestamp"`
}

// SecurityEventProvider is implemented by security plugins.
// The agent drains pending events and forwards them to the dashboard.
type SecurityEventProvider interface {
	// DrainSecurityEvents returns and clears the pending security events.
	DrainSecurityEvents() []SecurityEvent
}

// ============================================================
// BackupResult is a backup run result.
// ============================================================
type BackupResult struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"`
	Size       int64     `json:"size"`
	Error      string    `json:"error,omitempty"`
}

// ============================================================
// Config field self-declaration.
// Plugins declare which config fields they need; the frontend builds a form from it.
// ============================================================

// ConfigFieldType is the form input type.
type ConfigFieldType string

const (
	FieldText     ConfigFieldType = "text"
	FieldNumber   ConfigFieldType = "number"
	FieldSelect   ConfigFieldType = "select"
	FieldCheckbox ConfigFieldType = "checkbox"
	FieldPassword ConfigFieldType = "password"
	FieldTextarea ConfigFieldType = "textarea"
)

// ConfigField is a config field declared by a plugin.
// JSON tags are used directly by the frontend form.
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

// Validate checks a value against the field definition.
// Server-side validation is required; do not rely on the frontend.
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

// ConfigProvider lets a plugin declare its config fields (optional).
type ConfigProvider interface {
	GetConfigFields() []ConfigField
}

// ValidateAll validates all values against the field definitions.
func ValidateAll(fields []ConfigField, values map[string]string) error {
	for i := range fields {
		f := &fields[i]
		if err := f.Validate(values[f.Key]); err != nil {
			return err
		}
	}
	return nil
}

// ApplyDefaults fills default values for missing fields.
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
// Display name self-declaration (optional).
// ============================================================

// DisplayNameProvider lets a plugin declare a short display name.
// Plugins that implement it show this name; otherwise the frontend falls back to "PLUGIN".
type DisplayNameProvider interface {
	DisplayName() string
}
