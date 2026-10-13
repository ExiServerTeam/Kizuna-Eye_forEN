//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import "os"

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
