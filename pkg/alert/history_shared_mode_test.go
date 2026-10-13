//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package alert

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"Kizuna-Eye/pkg/notify"
)

// A-4: alert_history.jsonl はダッシュボード (= 運用ユーザー) が書き、agent
// (kizuna-agent) がプラグインの V2-B 整合性検証で読む。移行スクリプト
// (systemd/migrate-agent-user.sh §3.7) が与えた group read (0640) をダッシュ
// ボードが起動時・追記時・圧縮時に 0600 へ戻すと、agent はチェック間隔ごとに
// 「open failed: permission denied」の警告 (= alert_history_tamper) を出し続ける。
// どの経路でも 0640 を保つこと。
func TestHistoryKeepsSharedGroupReadMode(t *testing.T) {
	dir := t.TempDir()
	// 共有ログディレクトリ (移行後は 3770 = setgid + sticky + group rw)。
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	path := filepath.Join(dir, "alert_history.jsonl")

	// 1) 移行直後: 既存ファイルは 0640。起動時 (SetPersistence) に戻さない。
	if err := os.WriteFile(path, []byte("{}\n"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := NewHistory(10)
	h.SetPersistence(path)
	if perm := permOf(t, path); perm != 0o640 {
		t.Fatalf("SetPersistence: perm = %04o, want 0640", perm)
	}

	// 2) 追記しても 0640。
	h.Add(&notify.Alert{Type: "t", Level: notify.LevelInfo, Title: "a", Timestamp: time.Now()})
	if perm := permOf(t, path); perm != 0o640 {
		t.Fatalf("Add: perm = %04o, want 0640", perm)
	}

	// 3) 圧縮 (Load による超過分の切り詰め = WriteFileAtomic) でも 0640。
	h2 := NewHistory(1)
	if err := h2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if perm := permOf(t, path); perm != 0o640 {
		t.Fatalf("compaction: perm = %04o, want 0640", perm)
	}

	// 4) Clear (アーカイブ) 後の再作成も、共有ディレクトリなら 0640。
	if err := h2.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	h2.Add(&notify.Alert{Type: "t", Level: notify.LevelInfo, Title: "b", Timestamp: time.Now()})
	if perm := permOf(t, path); perm != 0o640 {
		t.Fatalf("recreate after Clear: perm = %04o, want 0640", perm)
	}
}

// 非共有ディレクトリ (0700) では従来どおり 0600 を作り、group へ広げないこと。
func TestHistoryStaysOwnerOnlyInPrivateDir(t *testing.T) {
	dir := t.TempDir()
	// testing.T.TempDir は互換性のため 0777 にするので、非共有を明示する。
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	path := filepath.Join(dir, "alert_history.jsonl")

	h := NewHistory(10)
	h.SetPersistence(path)
	h.Add(&notify.Alert{Type: "t", Level: notify.LevelInfo, Title: "a", Timestamp: time.Now()})
	if perm := permOf(t, path); perm != 0o600 {
		t.Fatalf("new file: perm = %04o, want 0600", perm)
	}

	// 既に group read があるファイルは、ディレクトリが 0700 でも 0640 を保つ
	// (オペレーターが明示的に与えた共有ビットをコード側で取り消さない)。
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	h.SetPersistence(path)
	if perm := permOf(t, path); perm != 0o640 {
		t.Fatalf("existing 0640: perm = %04o, want 0640", perm)
	}
}

func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	return info.Mode().Perm()
}
