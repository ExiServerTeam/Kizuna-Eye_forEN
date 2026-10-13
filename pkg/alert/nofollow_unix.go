//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package alert

import (
	"os"
	"syscall"
)

// openAppendNoFollow は最終コンポーネントのシンボリックリンクを辿らずに
// path を追記用に開く。O_NOFOLLOW が無いと、ログディレクトリにシンボリックリンクを
// 作れる（またはファイルを差し替えられる）攻撃者が、dashboard に任意ファイルへ
// 偽造エントリを追記させ得る（G-6）。
//
// perm はファイル作成時（O_CREATE）にのみ適用される。呼び出し側は
// fsutil.SharedFileMode を渡し、狭く共有された宛先（A-4）が移行で与えた
// グループ読み取りを保ち、所有者のみで再作成されないようにする。
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
