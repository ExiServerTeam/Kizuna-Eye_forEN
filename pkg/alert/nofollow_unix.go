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
func openAppendNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
}
