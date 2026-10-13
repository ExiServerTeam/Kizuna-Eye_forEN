//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"os"
	"syscall"
)

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
