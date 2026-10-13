//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import "os"

// fileInode は syscall.Stat_t を持たないプラットフォーム向けの移植可能な
// フォールバックだ。常に 0 を返すため、modules.json の監視はサイズと mtime の
// 比較のみにフォールバックする。
//
// agent は本番では Linux で動く。これはパッケージ（およびモジュール全体）が
// Windows 開発機やクロスコンパイル検査でも依然コンパイルでき、
// `go test ./...` が動くようにするために存在する。
// fileInode is the portable fallback for platforms without syscall.Stat_t.
// It always returns 0, so the modules.json watcher falls back to comparing
// size and mtime only.
//
// The agent runs on Linux in production; this exists so the package (and the
// whole module) still compiles and `go test ./...` still runs on a Windows
// developer machine and for cross-compiled checks.
func fileInode(fi os.FileInfo) uint64 {
	return 0
}
