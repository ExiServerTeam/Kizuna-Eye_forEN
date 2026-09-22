package status

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// ReadFromFile は指定されたファイルから SystemStatus を読み込む
// ファイルが存在しない場合は (nil, nil) を返す（エラー扱いにしない）
func ReadFromFile(path string) (*SystemStatus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // ファイルがなくてもエラーにしない
		}
		return nil, fmt.Errorf("ファイル読み取りエラー: %w", err)
	}

	return ReadFromJSON(data)
}

// ReadFromJSON は JSON バイト列から SystemStatus を読み込む
func ReadFromJSON(data []byte) (*SystemStatus, error) {
	var s SystemStatus
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("JSONパースエラー: %w", err)
	}
	return &s, nil
}

// ReadFromReader は io.Reader から SystemStatus を読み込む
func ReadFromReader(r io.Reader) (*SystemStatus, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("読み取りエラー: %w", err)
	}
	return ReadFromJSON(data)
}

// ReadFromFileWithDefault はファイルから読み込み、失敗した場合はデフォルト値を返す
// ファイルが存在しない場合やパースエラーの場合もデフォルトを返す
func ReadFromFileWithDefault(path string, defaultStatus *SystemStatus) (*SystemStatus, error) {
	s, err := ReadFromFile(path)
	if err != nil {
		return defaultStatus, err
	}
	if s == nil {
		return defaultStatus, nil
	}
	return s, nil
}

// Validate は SystemStatus の内容を検証する（簡易バリデーション）
// 必要に応じて各フィールドの範囲チェックなどを行う
func (s *SystemStatus) Validate() error {
	if s == nil {
		return fmt.Errorf("ステータスが nil です")
	}
	if s.Timestamp < 0 {
		return fmt.Errorf("Timestamp が不正です: %d", s.Timestamp)
	}
	if s.CPUUsage < 0 || s.CPUUsage > 100 {
		return fmt.Errorf("CPUUsage が範囲外です: %.2f", s.CPUUsage)
	}
	if s.MemPercent < 0 || s.MemPercent > 100 {
		return fmt.Errorf("MemPercent が範囲外です: %.2f", s.MemPercent)
	}
	if s.DiskPercent < 0 || s.DiskPercent > 100 {
		return fmt.Errorf("DiskPercent が範囲外です: %.2f", s.DiskPercent)
	}
	return nil
}

// IsStale は最終更新時刻から指定された期間以上経過しているかを判定する
// maxAge: 最大有効期間（秒）
func (s *SystemStatus) IsStale(maxAge int64) bool {
	if s == nil {
		return true
	}
	now := time.Now().Unix()
	return (now - s.Timestamp) > maxAge
}

// Clone は SystemStatus のディープコピーを返す
func (s *SystemStatus) Clone() *SystemStatus {
	if s == nil {
		return nil
	}
	copied := *s
	return &copied
}
