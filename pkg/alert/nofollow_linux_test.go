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

	if _, err := openAppendNoFollow(link); err == nil {
		t.Fatal("openAppendNoFollow must refuse to follow a symlink")
	}

	// 実ファイル（非 symlink）は通常どおり追記できる。
	f, err := openAppendNoFollow(target)
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
