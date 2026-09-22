package collector

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Task は定期的に実行する処理を表す
type Task struct {
	Name          string
	Interval      time.Duration
	Func          func(ctx context.Context) error
	AllowParallel bool // 【改善3】true: 並列実行許可 / false: 同一タスクの多重実行禁止
}

// TaskResult はタスクの実行結果を表す（【改善2】外部通知用）
type TaskResult struct {
	Name     string
	Duration time.Duration
	Err      error
	NextRun  time.Time // 【改善4】次回実行予定時刻
}

// Scheduler は複数のタスクを定期実行するスケジューラー
type Scheduler struct {
	mu          sync.Mutex
	name        string
	tasks       []*Task
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	running     bool
	logger      *log.Logger
	taskRunning sync.Map        // map[string]*atomic.Bool
	taskNextRun sync.Map        // map[string]time.Time（【改善4】次回実行予定時刻）
	resultCh    chan TaskResult // 【改善2】結果通知チャネル（nil の場合は通知なし）
}

// NewScheduler は新しい Scheduler を作成する
func NewScheduler(name string) *Scheduler {
	if name == "" {
		name = "Scheduler"
	}
	return &Scheduler{
		name:     name,
		tasks:    make([]*Task, 0),
		logger:   log.Default(),
		resultCh: nil, // デフォルトは通知なし
	}
}

// SetLogger はロガーを設定する
func (s *Scheduler) SetLogger(logger *log.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
}

// SetResultChannel は結果通知チャネルを設定する
// 外部で受信する場合はこのメソッドでチャネルを設定する（Start 前に呼び出すこと）
func (s *Scheduler) SetResultChannel(ch chan TaskResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resultCh = ch
}

// Register はタスクを登録する（Start 前に呼び出すこと）
func (s *Scheduler) Register(task *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 【改善1】Register 時に実行フラグを初期化
	s.taskRunning.Store(task.Name, &atomic.Bool{})
	// 【改善4】次回実行予定時刻を初期化（ゼロ値）
	s.taskNextRun.Store(task.Name, time.Time{})

	s.tasks = append(s.tasks, task)
}

// Start はすべてのタスクの定期実行を開始する
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	childCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	tasks := append([]*Task(nil), s.tasks...)
	s.mu.Unlock()

	for _, task := range tasks {
		s.wg.Add(1)
		go s.runTask(childCtx, task)
	}
	s.logger.Printf("[%s] 開始 (%d タスク登録済み)", s.name, len(tasks))
}

// Stop はすべてのタスクの実行を停止する
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	s.wg.Wait()

	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	s.logger.Printf("[%s] 停止", s.name)
}

// runTask は単一のタスクを定期実行するゴルーチン
func (s *Scheduler) runTask(ctx context.Context, task *Task) {
	defer s.wg.Done()

	ticker := time.NewTicker(task.Interval)
	defer ticker.Stop()

	// 初回即時実行
	s.runTaskOnce(ctx, task)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runTaskOnce(ctx, task)
		}
	}
}

// runTaskOnce はタスクを1回実行する
func (s *Scheduler) runTaskOnce(ctx context.Context, task *Task) {
	// 【改善3】並列実行制御（AllowParallel=false の場合のみ多重実行禁止）
	if !task.AllowParallel {
		val, ok := s.taskRunning.Load(task.Name)
		if !ok {
			// 念のため初期化（Register で既に存在するはず）
			val = &atomic.Bool{}
			s.taskRunning.Store(task.Name, val)
		}
		isRunning := val.(*atomic.Bool)
		if isRunning.Load() {
			s.logger.Printf("[%s] タスク '%s' は既に実行中のためスキップ", s.name, task.Name)
			// 【改善2】スキップした場合も結果を通知（エラーではないが、実行されなかったことを通知）
			s.sendResult(TaskResult{
				Name:     task.Name,
				Duration: 0,
				Err:      nil, // スキップはエラーではない
				NextRun:  s.getNextRun(task),
			})
			return
		}
		isRunning.Store(true)
		defer isRunning.Store(false)
	}

	// 実行時間計測
	start := time.Now()
	err := task.Func(ctx)
	duration := time.Since(start)

	// 【改善4】次回実行予定時刻を更新
	s.updateNextRun(task)

	// 結果通知
	s.sendResult(TaskResult{
		Name:     task.Name,
		Duration: duration,
		Err:      err,
		NextRun:  s.getNextRun(task),
	})

	if err != nil {
		s.logger.Printf("[%s] タスク '%s' 実行エラー (所要時間 %v): %v", s.name, task.Name, duration, err)
	} else {
		// 正常完了時は Debug レベルで出力（必要に応じてコメント解除）
		// s.logger.Printf("[%s] タスク '%s' 完了 (所要時間 %v)", s.name, task.Name, duration)
	}
}

// sendResult は結果チャネルに通知する（チャネルが設定されている場合のみ）
func (s *Scheduler) sendResult(result TaskResult) {
	s.mu.Lock()
	ch := s.resultCh
	s.mu.Unlock()

	if ch != nil {
		// 非ブロッキング送信（チャネルが溢れるのを防ぐ）
		select {
		case ch <- result:
		default:
			s.logger.Printf("[%s] 警告: 結果チャネルが満杯のため送信スキップ", s.name)
		}
	}
}

// updateNextRun は次回実行予定時刻を更新する
func (s *Scheduler) updateNextRun(task *Task) {
	s.taskNextRun.Store(task.Name, time.Now().Add(task.Interval))
}

// getNextRun は次回実行予定時刻を取得する
func (s *Scheduler) getNextRun(task *Task) time.Time {
	if val, ok := s.taskNextRun.Load(task.Name); ok {
		return val.(time.Time)
	}
	return time.Time{}
}

// GetNextRun は外部からタスクの次回実行予定時刻を取得する（ダッシュボード表示用）
func (s *Scheduler) GetNextRun(taskName string) time.Time {
	if val, ok := s.taskNextRun.Load(taskName); ok {
		return val.(time.Time)
	}
	return time.Time{}
}

// IsRunning はスケジューラーが稼働中かどうかを返す
func (s *Scheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// TaskCount は登録されているタスク数を返す
func (s *Scheduler) TaskCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tasks)
}
