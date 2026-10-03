//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package alert

import "os"

// openAppendNoFollow is the portable fallback for platforms without
// syscall.O_NOFOLLOW (the dashboard and agent run on Linux; this keeps the
// package buildable everywhere, e.g. for cross-compiled tests). See G-6.
func openAppendNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
}
