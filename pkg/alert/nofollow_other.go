//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package alert

import "os"

// openAppendNoFollow は syscall.O_NOFOLLOW を持たないプラットフォーム向けの
// 移植可能なフォールバックだ（dashboard と agent は Linux で動く。これは
// クロスコンパイルしたテストなどで、パッケージをどこでもビルド可能に保つ）。G-6 参照。
//
// perm はファイル作成時にのみ適用される。fsutil.SharedFileMode 参照。
// openAppendNoFollow is the portable fallback for platforms without
// syscall.O_NOFOLLOW (the dashboard and agent run on Linux; this keeps the
// package buildable everywhere, e.g. for cross-compiled tests). See G-6.
//
// perm is applied only when the file is created; see fsutil.SharedFileMode.
func openAppendNoFollow(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, perm)
}
