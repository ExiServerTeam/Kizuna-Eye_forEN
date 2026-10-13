//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"os"
	"syscall"
)

// fileInode は fi の inode 番号を返し、プラットフォームが公開していなければ
// 0 を返す。
//
// agent はこれを使い、modules.json が置き換えられた（rename / アトミック保存）
// ことを、新しいファイルがたまたま同じサイズで、同じ秒に丸められる mtime を
// 持つ場合でも検出する。サイズ+mtime だけの比較はそうした書き込みを見逃し、
// 次に実際の変更があるまでプラグイン一覧が黙って古いままになっていた。
// fileInode returns the inode number of fi, or 0 when the platform does not
// expose one.
//
// The agent uses it to detect that modules.json was replaced (rename / atomic
// save) even when the new file happens to have the same size and a mtime that
// rounds to the same second. Comparing only size+mtime missed those writes and
// the plugin list silently went stale until the next real change.
func fileInode(fi os.FileInfo) uint64 {
	if fi == nil {
		return 0
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
