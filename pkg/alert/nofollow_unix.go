//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package alert

import (
	"os"
	"syscall"
)

// openAppendNoFollow opens path for appending without following a symlink at
// the final component. Without O_NOFOLLOW, an attacker who can create a symlink
// in the log directory (or replace the file with one) could make the dashboard
// append forged entries to an arbitrary file (G-6).
//
// perm is applied only when the file is created (O_CREATE). Callers pass
// fsutil.SharedFileMode so a narrowly shared destination (A-4) keeps the group
// read the migration granted instead of being recreated owner-only.
func openAppendNoFollow(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, perm)
}
