//go:build linux

package main

import (
	"os"
	"syscall"
)

// inodeOf returns the inode number of fi. It is recorded in the directory watch
// baseline so a file that was replaced by a different one at the same path (same
// size, same mtime to the second) is still visible as a change. Platforms
// without syscall.Stat_t return 0 and fall back to size+mtime.
func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
