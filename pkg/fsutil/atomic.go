// Package fsutil provides small filesystem helpers shared by the dashboard,
// the agent, and the security plugin.
package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path atomically: the bytes go to a temporary
// file in the same directory, are fsynced, and are then renamed over path.
// A reader therefore never observes a half-written file, and a crash cannot
// leave a truncated config, baseline, or session file behind.
//
// perm is applied to the temporary file *before* the rename, so the final file
// never exists with a wider mode than requested (the previous hand-rolled
// versions in fim.go / session.go / api/config.go / api/modules.go each
// re-implemented this, and the plugin ones did not fsync at all).
//
// Two cases fail closed instead of writing:
//   - path is empty;
//   - path is an existing symlink. The baselines, state files and configs of a
//     hardened deployment live in directories that may be attacker-writable
//     (see the SMB exposure notes), and writing through a symlink would let
//     the writer pick the target.
//
// The parent directory is created with 0700 when it does not exist.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if path == "" {
		return errors.New("fsutil: 書き込み先パスが空です")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("fsutil: ディレクトリ作成失敗 (%s): %w", dir, err)
		}
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("fsutil: 書き込み先がシンボリックリンクです: %s", path)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fsutil: 一時ファイル作成失敗: %w", err)
	}
	name := tmp.Name()
	// Removes the temp file on every error path; a no-op after the rename.
	defer func() { _ = os.Remove(name) }()

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("fsutil: chmod 失敗: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("fsutil: 書き込み失敗: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsutil: fsync 失敗: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fsutil: close 失敗: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("fsutil: rename 失敗 (%s): %w", path, err)
	}
	return nil
}
