package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// FileLogger は検知したセキュリティイベントを JSON Lines 形式で記録する。
//
// 各行には前の行のハッシュ(prev_hash)と自身のハッシュ(hash)を付与する
// (ハッシュチェーン)。V1-A 以降、ハッシュは HMAC-SHA256（鍵付き）で計算する。
// 鍵を知らない攻撃者は、行を削除・改変してもチェーンを再計算できないため、
// 改ざんが VerifyChainKeyed で検出される。
//
// 鍵が空の場合（NewFileLogger）は従来の鍵なし SHA-256 にフォールバックする。
// これはテストと、旧形式ログの検証のために残している。
type FileLogger struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	key      []byte
	prevHash string
	// quarantine records that an existing log failed verification and was
	// moved aside at open time. High-5: this used to happen silently, so a
	// tampered log looked like a normal restart with an empty history.
	quarantine *chainQuarantine
}

// chainQuarantine describes an existing log that failed chain verification and
// was archived so that a fresh chain could start.
type chainQuarantine struct {
	Archive string
	Reason  string
}

// Quarantine returns the archive path and the verification error when an
// existing log was quarantined at open time, or ("", "") when none was.
// ParseConfig/Configure use it to raise a dashboard/discord alert.
func (l *FileLogger) Quarantine() (string, string) {
	if l == nil || l.quarantine == nil {
		return "", ""
	}
	return l.quarantine.Archive, l.quarantine.Reason
}

// quarantineExtra builds the extra fields of the chain_quarantined warning.
// archive_perm_error is present only when chmod 0600 on the archived log
// failed (Low-14), so an operator can spot a legacy log that is still
// readable by others.
func quarantineExtra(q *chainQuarantine, permErr string) map[string]interface{} {
	extra := map[string]interface{}{
		"archive": q.Archive,
		"reason":  q.Reason,
	}
	if permErr != "" {
		extra["archive_perm_error"] = permErr
	}
	return extra
}

// loadOrCreateChainKey は HMAC 鍵を path から読む。無ければ 32 バイトの
// ランダム鍵を生成して 0600 で保存する（親ディレクトリは 0700）。
func loadOrCreateChainKey(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("chain key path is empty")
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) == 0 {
			return nil, fmt.Errorf("chain key is empty: %s", path)
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		if os.IsExist(err) {
			return os.ReadFile(path)
		}
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

// NewFileLogger opens a log without a key (legacy / tests).
func NewFileLogger(path string) (*FileLogger, error) {
	return NewFileLoggerKeyed(path, "")
}

// NewFileLoggerKeyed opens a log and uses the HMAC key at keyPath for the chain.
// An empty keyPath keeps the legacy keyless chain.
func NewFileLoggerKeyed(path, keyPath string) (*FileLogger, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("ログディレクトリ作成失敗: %w", err)
		}
	}

	var key []byte
	// archivePermErr records a failed chmod on the quarantined legacy log so
	// the chain_quarantined warning reports it instead of staying silent
	// (Low-14).
	var archivePermErr string
	if keyPath != "" {
		k, err := loadOrCreateChainKey(keyPath)
		if err != nil {
			return nil, fmt.Errorf("チェーン鍵の読み込み/生成失敗: %w", err)
		}
		key = k
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("ログファイルオープン失敗: %w", err)
	}

	// 既存ログが旧形式や破損・鍵不一致だと検証が「改ざん」と誤判定する。
	// ファイル全体を検証し、無効なら退避ローテーションして新チェーンを開始する。
	var quarantined *chainQuarantine
	if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
		if verifyErr := VerifyChainKeyed(path, key); verifyErr != nil {
			_ = f.Close()
			archive := fmt.Sprintf("%s.legacy-%s", path, time.Now().Format("20060102_150405"))
			if renameErr := os.Rename(path, archive); renameErr != nil {
				return nil, fmt.Errorf("既存ログの退避に失敗しました: %w", renameErr)
			}
			// Low-14: the archived copy keeps the mode of the original file,
			// which may predate the 0600 tightening, and it still contains
			// usernames/IPs. Tighten it here; a failure is reported by the
			// chain_quarantined warning below (never silent).
			if permErr := os.Chmod(archive, 0600); permErr != nil {
				archivePermErr = permErr.Error()
			}
			quarantined = &chainQuarantine{Archive: archive, Reason: verifyErr.Error()}
			f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
			if err != nil {
				return nil, fmt.Errorf("ログファイル再作成失敗: %w", err)
			}
		}
	}

	l := &FileLogger{file: f, path: path, key: key, quarantine: quarantined}
	l.prevHash = loadLastHash(path)

	// High-5: 退避は「過去ログが改ざんされていた（または鍵が入れ替わった）」
	// という重大な事象である。黙って新チェーンで続行すると、改ざんの痕跡が
	// 消えたように見える。最低限、新しいログ自身にも残す（監査時に
	// 「いつ・どのファイルを・なぜ退避したか」を追えるようにする）。
	// ダッシュボード/Discord への通知は Configure が Quarantine() を見て行う。
	if quarantined != nil {
		l.Warn("chain_quarantined",
			fmt.Sprintf("既存ログの検証に失敗したため退避しました: %s", quarantined.Archive),
			quarantineExtra(quarantined, archivePermErr))
	}
	return l, nil
}

// chainHash は鍵なしの SHA-256（後方互換・テスト用）。
func chainHash(prevHash string, entry map[string]interface{}) (string, error) {
	return chainHashKeyed(nil, prevHash, entry)
}

// chainHashKeyed は鍵があれば HMAC-SHA256、無ければ SHA-256 で
// prevHash + canonical JSON を計算する。json.Marshal はキーをソートするので
// 正規形は決定的になる。
func chainHashKeyed(key []byte, prevHash string, entry map[string]interface{}) (string, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	var h hash.Hash
	if len(key) > 0 {
		h = hmac.New(sha256.New, key)
	} else {
		h = sha256.New()
	}
	h.Write([]byte(prevHash))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (l *FileLogger) log(level, event, message string, extra map[string]interface{}) {
	if l == nil || l.file == nil {
		return
	}

	entry := map[string]interface{}{
		"ts":      time.Now().Format(time.RFC3339),
		"level":   level,
		"event":   event,
		"message": message,
	}
	for k, v := range extra {
		entry[k] = v
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	entry["prev_hash"] = l.prevHash
	hash, err := chainHashKeyed(l.key, l.prevHash, entry)
	if err != nil {
		return
	}
	entry["hash"] = hash

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_, _ = l.file.Write(append(data, '\n'))
	l.prevHash = hash
}

func (l *FileLogger) Info(event, message string, extra map[string]interface{}) {
	l.log("INFO", event, message, extra)
}

func (l *FileLogger) Warn(event, message string, extra map[string]interface{}) {
	l.log("WARN", event, message, extra)
}

func (l *FileLogger) Error(event, message string, extra map[string]interface{}) {
	l.log("ERROR", event, message, extra)
}

func (l *FileLogger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

// loadLastHash は path の最後の有効行のハッシュを返す（無ければ ""）。
func loadLastHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e map[string]interface{}
		if json.Unmarshal(line, &e) == nil {
			if h, ok := e["hash"].(string); ok {
				last = h
			}
		}
	}
	return last
}

// VerifyChain は鍵なしでチェーンを検証する（後方互換・テスト用）。
func VerifyChain(path string) error {
	return VerifyChainKeyed(path, nil)
}

// VerifyChainKeyed はチェーンを検証する。鍵があれば HMAC、無ければ SHA-256。
// ファイルが無い場合は nil を返す。
func VerifyChainKeyed(path string, key []byte) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	prev := ""
	lineNo := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			return fmt.Errorf("line %d: invalid JSON: %w", lineNo, err)
		}
		got, _ := entry["hash"].(string)
		gotPrev, _ := entry["prev_hash"].(string)
		if gotPrev != prev {
			return fmt.Errorf("line %d: prev_hash mismatch", lineNo)
		}
		delete(entry, "hash")
		want, err := chainHashKeyed(key, prev, entry)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("line %d: hash mismatch", lineNo)
		}
		prev = got
	}
	return sc.Err()
}
