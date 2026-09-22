package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"Kizuna-Eye/pkg/status"
)

// FileTransport はファイルベースのトランスポートを実装する
type FileTransport struct {
	path        string       // ステータスファイルのパス
	mu          sync.RWMutex // ファイルアクセス排他用
	lastModTime time.Time    // 【改善4】前回の更新時刻（ポーリング用）
	logger      Logger       // ロガー（任意）
	maxFileSize int64        // 【改善3】最大ファイルサイズ（0=制限なし）
}

// Logger は簡易ロガーインターフェース
type Logger interface {
	Printf(format string, v ...interface{})
}

// Option は FileTransport の設定オプション
type Option func(*FileTransport)

// WithLogger はロガーを設定する
func WithLogger(logger Logger) Option {
	return func(f *FileTransport) {
		f.logger = logger
	}
}

// WithMaxFileSize は最大ファイルサイズを設定する（バイト単位）
// 0 の場合は制限なし
func WithMaxFileSize(size int64) Option {
	return func(f *FileTransport) {
		f.maxFileSize = size
	}
}

// NewFileTransport は新しい FileTransport を作成する
func NewFileTransport(path string, opts ...Option) *FileTransport {
	f := &FileTransport{
		path:        path,
		lastModTime: time.Time{},
		maxFileSize: 0, // デフォルトは制限なし
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// log はロガーが設定されていれば出力する
func (f *FileTransport) log(format string, v ...interface{}) {
	if f.logger != nil {
		f.logger.Printf("[FileTransport] "+format, v...)
	}
}

// Send はステータスをファイルに書き込む（エージェント側で使用）
func (f *FileTransport) Send(ctx context.Context, s *status.SystemStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// 【改善1】Marshal を使用（MarshalIndent より軽量）
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}

	// 【改善3】ファイルサイズチェック（制限を超える場合はローテーション）
	if f.maxFileSize > 0 && int64(len(data)) > f.maxFileSize {
		f.log("ファイルサイズ %d が制限 %d を超過。ローテーション実行", len(data), f.maxFileSize)
		if err := f.rotateFile(); err != nil {
			return fmt.Errorf("ローテーション失敗: %w", err)
		}
	}

	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// アトミック書き込み
	tmpFile := f.path + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, f.path)
}

// Receive はファイルからステータスを読み取る（ダッシュボード側で使用）
func (f *FileTransport) Receive(ctx context.Context) (*status.SystemStatus, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	data, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var s status.SystemStatus
	if err := json.Unmarshal(data, &s); err != nil {
		// 【改善2】ファイル破損検知：ログ出力 + 破損ファイルを退避
		f.log("JSON パースエラー: %v, ファイルを退避します", err)
		if renameErr := f.corruptFile(); renameErr != nil {
			f.log("破損ファイル退避失敗: %v", renameErr)
		}
		return nil, fmt.Errorf("JSON パースエラー: %w", err)
	}

	// 【改善4】最終更新時刻を更新（ReceiveWithPolling 用）
	f.mu.Lock()
	if info, err := os.Stat(f.path); err == nil {
		f.lastModTime = info.ModTime()
	}
	f.mu.Unlock()

	return &s, nil
}

// ReceiveWithPolling は指定された間隔でファイルをポーリングしながら
// ステータスを受信する（ファイルが存在するまで待機）
func (f *FileTransport) ReceiveWithPolling(ctx context.Context, interval time.Duration) (*status.SystemStatus, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 初回即時チェック
	if s, err := f.Receive(ctx); err != nil || s != nil {
		return s, err
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			s, err := f.Receive(ctx)
			if err != nil {
				return nil, err
			}
			if s != nil {
				return s, nil
			}
		}
	}
}

// WaitForUpdate はファイルが更新されるまで待機する（【改善4】更新検知版）
// 初回は即座にチェックし、以降は interval ごとに ModTime を比較する
// ファイルが存在しない場合も待機し続ける（作成されるまで待つ）
func (f *FileTransport) WaitForUpdate(ctx context.Context, interval time.Duration) (*status.SystemStatus, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 初期状態を記録（初回判定用）
	f.mu.Lock()
	f.lastModTime = time.Time{}
	f.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			// ファイルの更新時刻を取得
			info, err := os.Stat(f.path)
			if err != nil {
				if os.IsNotExist(err) {
					// ファイルがまだ存在しない → 待機継続
					continue
				}
				return nil, err
			}

			modTime := info.ModTime()

			f.mu.Lock()
			changed := f.lastModTime.IsZero() || modTime.After(f.lastModTime)
			if changed {
				f.lastModTime = modTime
			}
			f.mu.Unlock()

			if changed {
				// 更新があったので読み取り
				return f.Receive(ctx)
			}
		}
	}
}

// GetModTime はファイルの最終更新時刻を取得する
func (f *FileTransport) GetModTime() (time.Time, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	info, err := os.Stat(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// rotateFile はファイルをローテーションする（【改善3】）
// 例: status.json → status.json.1 → status.json.2 ...
func (f *FileTransport) rotateFile() error {
	// 最大3世代まで保持（簡易実装）
	const maxBackups = 3

	// 古いバックアップを削除（.3 を削除）
	oldPath := f.path + ".3"
	_ = os.Remove(oldPath)

	// シフト: .2 → .3, .1 → .2, .0 → .1
	for i := maxBackups - 1; i >= 1; i-- {
		src := f.path + "." + fmt.Sprintf("%d", i-1)
		dst := f.path + "." + fmt.Sprintf("%d", i)
		_ = os.Rename(src, dst)
	}

	// 現在のファイルを .0 にリネーム
	if err := os.Rename(f.path, f.path+".0"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// corruptFile は破損ファイルを退避する（【改善2】）
func (f *FileTransport) corruptFile() error {
	corruptPath := f.path + ".corrupt." + time.Now().Format("20060102150405")
	return os.Rename(f.path, corruptPath)
}

// Close は Closer インターフェース実装（何もしない）
func (f *FileTransport) Close() error {
	return nil
}

// コンパイル時のインターフェース適合確認
var (
	_ Sender   = (*FileTransport)(nil)
	_ Receiver = (*FileTransport)(nil)
	_ Closer   = (*FileTransport)(nil)
)
