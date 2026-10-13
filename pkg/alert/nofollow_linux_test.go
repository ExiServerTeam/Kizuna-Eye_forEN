//go:build linux

package alert

import (
	"os"
	"path/filepath"
	"testing"
)

// G-6: ログディレクトリにシンボリックリンクを置かれても、それを辿って
// 任意のファイルへ追記しないこと（O_NOFOLLOW）。
func TestOpenAppendNoFollowRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.log")
	if err := os.WriteFile(target, []byte("orig\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alert_history.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := openAppendNoFollow(link, 0600); err == nil {
		t.Fatal("openAppendNoFollow must refuse to follow a symlink")
	}

	// 実ファイル（非 symlink）は通常どおり追記できる。
	f, err := openAppendNoFollow(target, 0600)
	if err != nil {
		t.Fatalf("regular file should open: %v", err)
	}
	if _, err := f.Write([]byte("more\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// A-4: 新規作成時は渡したモードが適用される（共有ログディレクトリでは
// fsutil.SharedFileMode が 0640 を返すため、agent が読める）。
func TestOpenAppendNoFollowCreatesWithMode(t *testing.T) {
	dir := t.TempDir()
	newFile := filepath.Join(dir, "created.jsonl")
	f, err := openAppendNoFollow(newFile, 0o640)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	info, err := os.Stat(newFile)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("perm = %04o, want 0640", perm)
	}
}
