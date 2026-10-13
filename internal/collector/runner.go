package collector

import (
	"context"
	"log"
	"time"

	"Kizuna-Eye/pkg/status"
)

// CollectorFunc はシステム情報を取得する関数の型
type CollectorFunc func() *status.SystemStatus

// Runner は収集ループを実行する構造体
type Runner struct {
	interval            time.Duration
	collector           CollectorFunc
	stopOnCallbackError bool // true: コールバックエラーで停止 / false: ログ出力して継続
	enablePanicRecovery bool // true: panic を recover する
}

// NewRunner は新しい Runner を作成する
// stopOnCallbackError: callback がエラーを返した場合に停止するか継続するか
// enablePanicRecovery: collector の panic を捕捉して継続するか
func NewRunner(interval time.Duration, collector CollectorFunc, stopOnCallbackError bool, enablePanicRecovery bool) *Runner {
	return &Runner{
		interval:            interval,
		collector:           collector,
		stopOnCallbackError: stopOnCallbackError,
		enablePanicRecovery: enablePanicRecovery,
	}
}

// Run は収集ループを開始する
// ctx がキャンセルされると停止する
func (r *Runner) Run(ctx context.Context, callback func(*status.SystemStatus) error) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// 初回即時実行
	if err := r.safeRun(callback); err != nil {
		if r.stopOnCallbackError {
			return err
		}
		log.Printf("[RUNNER] 初回実行エラー (継続): %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.safeRun(callback); err != nil {
				if r.stopOnCallbackError {
					return err
				}
				log.Printf("[RUNNER] コールバックエラー (継続): %v", err)
			}
		}
	}
}

// safeRun は collector を panic 保護付きで実行し、callback を呼び出す
func (r *Runner) safeRun(callback func(*status.SystemStatus) error) error {
	// 1. collector を実行 (panic 保護)
	var s *status.SystemStatus
	var err error

	if r.enablePanicRecovery {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("[RUNNER] collector panic を捕捉: %v", rec)
					err = &panicError{value: rec}
				}
			}()
			s = r.collector()
		}()
	} else {
		s = r.collector()
	}

	if err != nil {
		return err
	}

	// 2. callback 実行
	return callback(s)
}

// panicError は panic を error として扱うための型
type panicError struct {
	value interface{}
}

func (e *panicError) Error() string {
	return "panic: " + e.value.(string) // 型アサーションは簡易実装
}
