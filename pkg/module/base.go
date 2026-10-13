package module

import (
	"context"
	"fmt"
	"regexp"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

// ============================================================
// Module は全モジュールが実装する基底インターフェースだ。
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
// Logger はログ出力のインターフェースだ。
// Logger is the logging interface.
// ============================================================
type Logger interface {
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
	Debug(format string, args ...interface{})
}

// ============================================================
// ModuleManager はモジュールを管理する。
// ModuleManager manages modules.
// ============================================================
type ModuleManager interface {
	Register(ctx context.Context, module Module) error
	// Unregister は単一モジュールの実行ループ（実行中なら）を停止し、取り除く。
	// プラグインが無効化・削除されたときに agent 再起動なしで実行を止めるために使う。
	// プラグインはメモリからアンロードできない（Go の制約）が、実行ループと
	// コールバックは停止する。
	// Unregister stops a single module's run loop (if running) and removes it.
	// Used when a plugin is disabled or removed so it stops executing without
	// an agent restart. Plugins cannot be unloaded from memory (Go limitation),
	// but their run loop and callbacks stop.
	Unregister(name string) error
	Get(name string) (Module, bool)
	Start(ctx context.Context)
	Stop()
	// NotifyConfigChanged はモジュールの実行ループに interval の再読み取りを促す。
	// これにより設定変更が再起動なしで反映される。
	// NotifyConfigChanged asks the module's run loop to re-read its
	// interval, so a config change takes effect without a restart.
	NotifyConfigChanged(name string)
}

type moduleManager struct {
	mu      sync.RWMutex
	modules map[string]Module
	resets  map[string]chan struct{}      // モジュール名 -> interval リセット信号
	cancels map[string]context.CancelFunc // モジュール名 -> モジュール単位の cancel
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
		cancels: make(map[string]context.CancelFunc),
		logger:  logger,
	}
}

func (m *moduleManager) Register(ctx context.Context, module Module) error {
	m.mu.Lock()
	// Name() はプラグインのコードだ。Register が最初に触る箇所なので、
	// ここで panic すると他の安全網が走る前に agent が落ちる。
	// Name() is plugin code. It is the first thing Register touches, so a
	// panic here would crash the agent before any other safety net runs.
	name := safeName(module)
	if name == "" {
		m.mu.Unlock()
		return fmt.Errorf("module returned an empty Name()（プラグインの Name() が空か、パニックしました）")
	}
	if _, exists := m.modules[name]; exists {
		m.mu.Unlock()
		return fmt.Errorf("module %s already registered", name)
	}
	if err := safeInit(ctx, module, m.logger); err != nil {
		m.mu.Unlock()
		return err
	}
	m.modules[name] = module
	if m.resets[name] == nil {
		m.resets[name] = make(chan struct{}, 1)
	}
	// マネージャが既に実行中なら、このモジュールのループを即座に開始する。
	// Start() 後に登録されたモジュール（例: ホットリロードされたプラグイン）が
	// そうしないと永遠に動かないためだ。
	// If the manager is already running, start this module's loop immediately.
	// Modules registered after Start() (e.g. hot-reloaded plugins) would
	// otherwise never run.
	running := m.running
	runCtx := m.runCtx
	if running && runCtx != nil {
		// このモジュール専用のキャンセル可能コンテキストを与え、マネージャ全体を止めずに
		// Unregister で個別に停止できるようにする。
		// Give this module its own cancellable context so it can be stopped
		// individually via Unregister without stopping the whole manager.
		modCtx, cancel := context.WithCancel(runCtx)
		m.cancels[name] = cancel
		m.wg.Add(1)
		go m.runModuleLoop(modCtx, module)
	}
	m.mu.Unlock()
	return nil
}

// Unregister は単一モジュールを停止し取り除く。冪等だ。未知の名前はエラーにしない。
// プラグインはメモリからアンロードできない（Go の制約）が、実行ループをキャンセルして
// 実行を止める。
// Unregister stops and removes a single module. It is idempotent: an unknown
// name is not an error. Plugins cannot be unloaded from memory (Go limitation),
// but their run loop is cancelled so they stop executing.
func (m *moduleManager) Unregister(name string) error {
	m.mu.Lock()
	mod, ok := m.modules[name]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	cancel := m.cancels[name]
	delete(m.modules, name)
	delete(m.resets, name)
	delete(m.cancels, name)
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// 対応しているモジュールにはベストエフォートで graceful stop を行う。
	// Stop はプラグインのコードなので recover で保護する。ここで panic すると
	// プラグインをアンロード中の agent が落ちてしまう。
	// Best-effort graceful stop for modules that support it. Guarded by
	// recover because Stop is plugin code and a panic here must not crash
	// the agent while it is unloading a plugin.
	if s, ok := mod.(interface{ Stop() error }); ok {
		func() {
			defer func() {
				if r := recover(); r != nil {
					if m.logger != nil {
						m.logger.Error("モジュール '%s' の Stop がパニックしました: %v", name, r)
					}
				}
			}()
			_ = s.Stop()
		}()
	}
	if m.logger != nil {
		m.logger.Info("モジュールを停止・解除しました: %s", name)
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
	// 並行する Register との競合を避けるため、ロック下でモジュールをスナップショットする。
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
		// safeName: Name() はプラグインのコードだ。ここで panic すると起動時に
		// agent 全体が落ちる。Name() が panic するモジュールはスキップする
		// （安全な名前で登録されているため、cancels も同じキーで扱う）。
		// safeName: Name() is plugin code; a panic here would crash the
		// whole agent at startup. A module whose Name() panics is skipped
		// (it was registered under the safe name, so cancels is keyed the
		// same way).
		name := safeName(mod)
		if name == "" {
			continue
		}
		modCtx, modCancel := context.WithCancel(ctx)
		m.mu.Lock()
		m.cancels[name] = modCancel
		m.mu.Unlock()
		m.wg.Add(1)
		go m.runModuleLoop(modCtx, mod)
	}
}

// safeName は panic 回復付きで mod.Name() を返す。Name() はプラグインのコードで、
// 他の安全網の前（または内部）で呼ばれるため、ここで panic するプラグインが
// agent を落としてならない。panic 時は空文字列を返し、呼び出し側が不正な名前で
// 登録する代わりにモジュールを拒否できるようにする。
// safeName returns mod.Name() with panic recovery. Name() is plugin code and
// is called before (or inside) other safety nets, so a plugin that panics here
// must not crash the agent. The empty string is returned on panic so the
// caller can reject the module instead of registering it under a bad name.
func safeName(mod Module) (name string) {
	defer func() {
		if r := recover(); r != nil {
			name = ""
		}
	}()
	return mod.Name()
}

// safeInterval は panic 回復付きで mod.Interval() を返す。panic は「interval 無し」(0) と
// 扱い、プロセスを落とす代わりにこのモジュールの実行ループだけを止める。
// safeInterval returns mod.Interval() with panic recovery. A panic is treated
// as "no interval" (0), which stops the run loop for this one module rather
// than taking down the process.
func safeInterval(mod Module) (d time.Duration) {
	defer func() {
		if r := recover(); r != nil {
			d = 0
		}
	}()
	return mod.Interval()
}

// safeInit は panic 回復付きでモジュールの Init を呼ぶ。Init で panic する
// サードパーティ製プラグインが agent を落としてならない（この時点で Register は
// マネージャロックを保持している）。panic をエラーへ変換し、登録がきれいに失敗するようにする。
// safeInit calls a module's Init with panic recovery. A third-party plugin
// that panics in Init must not crash the agent (Register holds the manager
// lock at this point). The panic is converted into an error so registration
// fails cleanly instead.
func safeInit(ctx context.Context, mod Module, lg Logger) (err error) {
	defer func() {
		if r := recover(); r != nil {
			// safeName を使う。recover ハンドラ自体が panic する Name() を呼んではならない
			// （recover 内の panic は回復されず agent が落ちる）。
			// Use safeName: the recover handler itself must not call a
			// panicking Name() (a panic inside recover is unrecovered and
			// would crash the agent).
			name := safeName(mod)
			if lg != nil {
				lg.Error("モジュール '%s' の Init がパニックしました: %v\n%s", name, r, debug.Stack())
			}
			err = fmt.Errorf("module %s: Init panicked: %v", name, r)
		}
	}()
	return mod.Init(ctx)
}

// safeRun は panic 回復付きで mod.Run を呼ぶ。プラグインはサードパーティの .so コードだ。
// Run の panic が agent 全体（そして他の全プラグインとシステム監視）を落としてならない。
// 回復した panic はログに残して障害を可視化し、実行ループは次の interval で継続する。
// safeRun invokes mod.Run with panic recovery. Plugins are third-party .so
// code; a panic in Run must not take down the whole agent (and with it every
// other plugin and the system monitoring). A recovered panic is logged so the
// fault is visible, and the run loop continues on the next interval.
func (m *moduleManager) safeRun(ctx context.Context, mod Module) {
	defer func() {
		if r := recover(); r != nil {
			if m.logger != nil {
				m.logger.Error("モジュール '%s' の Run がパニックしました: %v\n%s", safeName(mod), r, debug.Stack())
			}
		}
	}()
	mod.Run(ctx)
}

func (m *moduleManager) runModuleLoop(ctx context.Context, mod Module) {
	defer m.wg.Done()
	// Interval() と Name() は専用ゴルーチンで動くプラグインのコードだ。
	// ここで panic すると回復されずプロセス全体（そして他の全プラグインとシステム監視）が
	// 終了する。panic 安全なラッパー経由で呼ぶ。
	// Interval() and Name() are plugin code running in their own goroutine: a
	// panic here is unrecovered and would terminate the whole process (and
	// with it every other plugin and the system monitoring). Call them through
	// the panic-safe wrappers.
	if safeInterval(mod) <= 0 {
		return
	}

	m.mu.RLock()
	resetCh := m.resets[safeName(mod)]
	m.mu.RUnlock()

	m.safeRun(ctx, mod)

	for {
		// 設定変更を反映するため、毎サイクル interval を読み直す。
		// Re-read the interval every cycle so a config change takes effect.
		interval := safeInterval(mod)
		if interval <= 0 {
			return
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-resetCh:
			// interval が変わった。今は実行せず再評価する。
			// Interval changed; re-evaluate without running now.
			timer.Stop()
		case <-timer.C:
			m.safeRun(ctx, mod)
		}
	}
}

// NotifyConfigChanged はモジュールの実行ループを起こし、interval を読み直させる。
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
	for name, c := range m.cancels {
		if c != nil {
			c()
		}
		delete(m.cancels, name)
	}
	m.mu.Unlock()

	m.wg.Wait()
	if m.logger != nil {
		m.logger.Info("ModuleManager stopped")
	}
}

// ============================================================
// プラグイン（.so）と agent の間で共有するバックアップ型。
// Backup types shared between plugins (.so) and the agent.
// ============================================================

// BackupStatus は最新のバックアップ実行結果だ。
// BackupStatus is the latest backup run result.
type BackupStatus struct {
	Name    string `json:"name,omitempty"`
	LastRun string `json:"last_run,omitempty"`
	Status  string `json:"status,omitempty"`
	Size    int64  `json:"size,omitempty"`
	NextRun string `json:"next_run,omitempty"`
}

// BackupStatusProvider はバックアッププラグインが実装する。
// BackupStatusProvider is implemented by backup plugins.
type BackupStatusProvider interface {
	GetBackupStatus() *BackupStatus
}

// ============================================================
// BackupRunner はバックアップを実行できるプラグインが実装する。
// BackupRunner is implemented by plugins that can run backups.
// ============================================================
type BackupRunner interface {
	RunBackup(ctx context.Context) error
}

// ============================================================
// セキュリティ型: セキュリティプラグイン（.so）と agent の間で共有する。
// Security types shared between security plugins (.so) and the agent.
// ============================================================

// SecurityEvent はプラグインが検知した 1 件のセキュリティ関連イベントだ。
// SecurityEvent is one security-relevant event detected by a plugin.
type SecurityEvent struct {
	Plugin   string `json:"plugin"`
	Category string `json:"category"` // ssh_login / ssh_failed / sudo / install / account / config
	Level    string `json:"level"`    // critical / warning / info
	Title    string `json:"title"`
	Message  string `json:"message"`
	// TitleEN / MessageEN は Title/Message の英語レンダリングを持つ。
	// これにより UI はキーから文字列を再生成せず、選択言語でログとアラート履歴を表示できる。
	// 古いレコードには無い。その場合 UI は Title/Message（日本語）にフォールバックする。
	// TitleEN / MessageEN carry the English rendering of Title/Message so
	// the UI can show the log and alert history in the selected language
	// without re-deriving the string from a key. Older records omit them;
	// the UI falls back to Title/Message (Japanese).
	TitleEN   string    `json:"title_en,omitempty"`
	MessageEN string    `json:"message_en,omitempty"`
	Source    string    `json:"source,omitempty"` // イベントの出所ログファイル
	Actor     string    `json:"actor,omitempty"`  // 操作を実行したユーザー
	IP        string    `json:"ip,omitempty"`     // 送信元 IP アドレス
	Timestamp time.Time `json:"timestamp"`

	// --- アラート詳細表示用 (タスク1) ---
	// Command はイベントを引き起こしたコマンド行だ（復元できる場合。cron エントリ、
	// SUID ファイルパスなど）。
	// Command is the command line that triggered the event, when it can be
	// recovered (cron entry, SUID file path, etc.).
	Command string `json:"command,omitempty"`
	// DetectFile は検知ロジックを含むプラグインのソースファイルだ（例: "cronmon.go"）。
	// DetectFile is the plugin source file that contains the detection logic
	// (e.g. "cronmon.go").
	DetectFile string `json:"detect_file,omitempty"`
	// DetectLine は DetectFile 内の検知ロジックの 1 始まり行番号だ。
	// DetectLine is the 1-based line number of the detection logic in
	// DetectFile.
	DetectLine int `json:"detect_line,omitempty"`
	// Remediation は人間が読める推奨アクション（提案された修正）だ。
	// Remediation is a human-readable suggested action (proposed fix).
	Remediation string `json:"remediation,omitempty"`
	// RelatedLog は検知の基になった生のログ行（または代表的な抜粋）だ。
	// RelatedLog is the raw log line (or a representative excerpt) that the
	// detection was based on.
	RelatedLog string `json:"related_log,omitempty"`

	// Extra はプラグインがセキュリティログに入れたい追加の構造化フィールド
	// （例: dedup_key、count）を持つ。後で jq で集計するためだ。キーは標準フィールドと
	// 並んでそのまま書かれる。
	// Extra carries additional structured fields (e.g. dedup_key, count)
	// that the plugin wants in the security log for later aggregation with
	// jq. Keys are written verbatim alongside the standard fields.
	Extra map[string]string `json:"extra,omitempty"`
}

// SecurityEventProvider はセキュリティプラグインが実装する。
// agent は保留中のイベントを排出し、dashboard へ転送する。
// SecurityEventProvider is implemented by security plugins.
// The agent drains pending events and forwards them to the dashboard.
type SecurityEventProvider interface {
	// DrainSecurityEvents は保留中のセキュリティイベントを返し、クリアする。
	// DrainSecurityEvents returns and clears the pending security events.
	DrainSecurityEvents() []SecurityEvent
}

// SecurityEventRequeuer はセキュリティプラグインが任意で実装する。
// これにより agent は配送できなかったイベント（例: 一時的な WebSocket 書き込みエラー）を
// 失う代わりに戻せる。
// SecurityEventRequeuer is optionally implemented by security plugins so the
// agent can put back events that could not be delivered (e.g. a transient
// WebSocket write error), instead of losing them.
type SecurityEventRequeuer interface {
	RequeueSecurityEvents(events []SecurityEvent)
}

// ============================================================
// BackupResult はバックアップ実行の結果だ。
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
// 設定フィールドの自己宣言。
// プラグインは必要な設定フィールドを宣言し、フロントエンドがそれからフォームを構築する。
// Config field self-declaration.
// Plugins declare which config fields they need; the frontend builds a form from it.
// ============================================================

// ConfigFieldType はフォーム入力の種別だ。
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

// ConfigField はプラグインが宣言する設定フィールドだ。
// JSON タグはフロントエンドのフォームが直接使う。
//
// *EN フィールドは任意の英語レンダリングだ。dashboard UI が英語に切り替わると、
// フロントエンドはそれらを優先し、空なら日本語フィールドにフォールバックする。
// したがって翻訳しないプラグインも使える。完全に英語の UI を望むプラグインは
// 4 つすべて（LabelEN / HintEN / PlaceholderEN / GroupEN）を埋める必要がある。
// ConfigField is a config field declared by a plugin.
// JSON tags are used directly by the frontend form.
//
// The *EN fields are optional English renderings. When the dashboard UI is
// switched to English, the frontend prefers them and falls back to the
// Japanese field when they are empty, so a plugin that does not translate is
// still usable. A plugin that wants a fully English UI must fill all four
// (LabelEN / HintEN / PlaceholderEN / GroupEN).
type ConfigField struct {
	Key           string          `json:"key"`
	Label         string          `json:"label"`
	LabelEN       string          `json:"label_en,omitempty"`
	Type          ConfigFieldType `json:"type"`
	Default       string          `json:"default,omitempty"`
	Required      bool            `json:"required,omitempty"`
	Options       []string        `json:"options,omitempty"`
	Placeholder   string          `json:"placeholder,omitempty"`
	PlaceholderEN string          `json:"placeholder_en,omitempty"`
	Hint          string          `json:"hint,omitempty"`
	HintEN        string          `json:"hint_en,omitempty"`
	Group         string          `json:"group,omitempty"`
	GroupEN       string          `json:"group_en,omitempty"`
	Min           *float64        `json:"min,omitempty"`
	Max           *float64        `json:"max,omitempty"`
	Pattern       string          `json:"pattern,omitempty"`
}

// Validate はフィールド定義に対して値を検証する。
// サーバー側の検証は必須だ。フロントエンドに頼ってはならない。
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

// ConfigProvider はプラグインが設定フィールドを宣言できるようにする（任意）。
// ConfigProvider lets a plugin declare its config fields (optional).
type ConfigProvider interface {
	GetConfigFields() []ConfigField
}

// ValidateAll はフィールド定義に対してすべての値を検証する。
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

// ApplyDefaults は未設定フィールドに既定値を埋める。
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
// 表示名の自己宣言（任意）。
// Display name self-declaration (optional).
// ============================================================

// DisplayNameProvider はプラグインが短い表示名を宣言できるようにする。
// 実装したプラグインはこの名前を表示し、そうでなければフロントエンドは "PLUGIN" に
// フォールバックする。
// DisplayNameProvider lets a plugin declare a short display name.
// Plugins that implement it show this name; otherwise the frontend falls back to "PLUGIN".
type DisplayNameProvider interface {
	DisplayName() string
}

// ============================================================
// タグの自己宣言（任意）。
// Tag self-declaration (optional).
// ============================================================

// TagProvider はプラグインがカードのバッジとして表示される短いタグを宣言できるようにする
// （例: Kizuna-Security なら "GUARD"、バックアップ変種なら "LITE"/"PRO"）。
// 値はプラグイン自身が決めるため、どうラベル付けするかは UI ではなくプラグインが
// 唯一の真実の源だ。実装しないプラグインは "PLUGIN" にフォールバックする。
//
// 検査ツール（cmd/plugin-inspect）はこのメソッドをリフレクションで読むため、
// プラグインは `Tag() string` メソッドを追加するだけでよく、呼び出しのために
// このパッケージをインポートする必要はない。
// TagProvider lets a plugin declare the short tag shown as a badge on its
// card (e.g. "GUARD" for Kizuna-Security, "LITE"/"PRO" for backup
// variants). The value is what the plugin itself decides, so the plugin —
// not the UI — is the single source of truth for how it is labelled.
// A plugin that does not implement it falls back to "PLUGIN".
//
// The inspector (cmd/plugin-inspect) reads this method by reflection, so a
// plugin only has to add a `Tag() string` method; it does not have to import
// this package for the call to work.
type TagProvider interface {
	Tag() string
}

// ============================================================
// ダッシュボードカードの自己宣言（任意）。
// Dashboard card self-declaration (optional).
// ============================================================

// DashboardCard はモジュールが dashboard に提供する 1 枚のカードを宣言する。
// コアは既知の Kind をレンダリングし、カードの有無・タイトル・最小ロールは
// モジュールが決める。したがってカードはコアにハードコードされるのではなく
// モジュールが追加する。
//
// Kind はコアがレンダリング方法を知っている小さな閉集合だ。
//   - "insight": コアが /api/insight で提供する集約セキュリティ状態
//     （脅威指数 + 送信元アクティビティ + 設定点検）。
//     セキュリティ型モジュールがこのカードを宣言する。集約自体はコアに残り
//     （観測と可視化）、モジュールは「表示せよ」と言うだけだ。
//
// DashboardCardProvider を実装しないモジュールはカードを提供しない。
// DashboardCard declares one card a module contributes to the dashboard.
// The core renders a known Kind; the module decides whether the card exists,
// its title and the minimum role, so the card is added by the module rather
// than hardcoded in the core.
//
// Kind is a small closed set the core knows how to render:
//   - "insight": the aggregated security status served by the core at
//     /api/insight (threat index + source activity + configuration check).
//     A security-type module declares this card; the aggregation itself stays
//     in the core (観測と可視化), the module only says "show it".
//
// A module that does not implement DashboardCardProvider contributes no card.
type DashboardCard struct {
	// ID は安定した識別子だ（例: "security"）。並び順のキーに使う。
	// ID is a stable identifier (e.g. "security"). Used for ordering keys.
	ID string `json:"id"`
	// Title / TitleEN はカード見出しだ。UI が英語のときは TitleEN を使い、
	// 空なら Title にフォールバックする。
	// Title / TitleEN are the card heading. TitleEN is used when the UI is
	// in English; an empty TitleEN falls back to Title.
	Title   string `json:"title"`
	TitleEN string `json:"title_en,omitempty"`
	// Kind はコアのレンダラを選ぶ（型のドキュメント参照）。
	// Kind selects the core renderer (see the type doc).
	Kind string `json:"kind"`
	// MinRole はカードを閲覧できる最小ロールだ（viewer | operator | admin）。
	// 空は operator を意味する。
	// MinRole is the minimum role that may see the card
	// (viewer | operator | admin). Empty means operator.
	MinRole string `json:"min_role,omitempty"`
}

// DashboardCardProvider はモジュールが dashboard カードを宣言できるようにする（任意）。
// 検査ツール（cmd/plugin-inspect）はこのメソッドをリフレクションで読むため、
// プラグインは `DashboardCards() []module.DashboardCard` メソッドを追加するだけでよく、
// 呼び出し自体はリフレクションベースだ。
// DashboardCardProvider lets a module declare dashboard cards (optional).
// The inspector (cmd/plugin-inspect) reads this method by reflection, so a
// plugin only has to add a `DashboardCards() []module.DashboardCard` method;
// the call itself is reflection-based.
type DashboardCardProvider interface {
	DashboardCards() []DashboardCard
}

// ============================================================
// Web アセットの自己宣言（任意）。
// Web asset self-declaration (optional).
// ============================================================

// WebAssetProvider はモジュールが独自の Web UI を同梱できるようにする。
// したがってカードはコアではなくモジュール（その HTML/JS/CSS）が所有する。
// コアは汎用の iframe ホストを提供し、アセットを配信するだけだ。これは設計 5.4
// （カード/詳細ページはモジュール側が追加する）に合致する。
//
// WebAssets は path -> content のマップを返す。パスは UI ルートからの相対で、
// "/" を使う（例: "index.html"、"card.js"、"style.css"）。コアはそれらを
// plugins/<name>.web/ の下へ展開し、/plugins/<name>/ で配信する。ルートを脱出する
// パス（".."、絶対パス、"\\"）は展開側が拒否する。
//
// WebUIPath は UI ルートからの相対のエントリ文書を返す（通常は "index.html"）。
// dashboard はそれをカードにマウントする。空はモジュールが UI を同梱しない
// （宣言のみのカード）ことを意味する。
//
// コアは小さな JSON 設定を UI へ渡す（dashboard のドキュメント参照）ため、
// モジュールはコアの API を知る必要がなく、与えられたものをレンダリングするだけだ。
// 特別なものをレンダリングしない素のモジュールは DashboardCards() だけを実装し、
// このインターフェースをスキップできる。
// WebAssetProvider lets a module ship its own web UI. The card is therefore
// owned by the module (its HTML/JS/CSS), not by the core: the core only
// provides a generic iframe host and serves the assets. This matches design
// 5.4 (カード/詳細ページはモジュール側が追加する).
//
// WebAssets returns a path -> content map. Paths are relative to the UI root
// and use "/" (e.g. "index.html", "card.js", "style.css"). The core extracts
// them under plugins/<name>.web/ and serves them at /plugins/<name>/. A path
// that escapes the root ("..", absolute, "\\") is rejected by the extractor.
//
// WebUIPath returns the entry document relative to the UI root (usually
// "index.html"); the dashboard mounts it in the card. Empty means the module
// ships no UI (a declaration-only card).
//
// The core passes a small JSON config to the UI (see the dashboard docs) so a
// module does not need to know the core's API; it only renders what it is
// given. Plain modules that render nothing special can implement only
// DashboardCards() and skip this interface.
type WebAssetProvider interface {
	// WebUIPath は UI ルートからの相対のエントリ文書だ。
	// WebUIPath is the entry document, relative to the UI root.
	WebUIPath() string
	// WebAssets は UI の path -> content マップだ。
	// WebAssets is the path -> content map of the UI.
	WebAssets() map[string][]byte
}
