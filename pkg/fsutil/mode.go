package fsutil

import (
	"os"
	"path/filepath"
)

// TightenSharedConfigMode は path のモードを所有者のみ（0600）へ書き換えるが、
// 意図的に与えられたグループ読み取りビット（0640）は保つ。
//
// なぜ存在するか（A-4）: agent は専用システムユーザー（kizuna-eye）で動き、
// dashboard（＝データディレクトリを所有する運用者）はログインアカウントで動き続ける。
// agent は dashboard が保存のたびに書き換える共有設定（agent_config.json、
// modules.json）を読む必要がある。読み込み時に一律 chmod 0600 すると、
// dashboard が再び触れた瞬間に agent がそれらを読めなくなり、移行手順が設定した
// グループビットを黙って取り消してしまう。"グループ読み取りのみ"を保つことで、
// 共有読み取りの契約を維持しつつ、それより広いものはすべて閉じる:
//
//	0644, 0666, 0660   -> 0640
//	0600, 0640, 0700   -> unchanged (0640) / 0600
//	0777               -> 0640
//	0400, 0000         -> 0600
//
// 置き換える呼び出しと同様にベストエフォートだ。chmod を尊重しないファイルシステム
// （一部の SMB マウント）や stat できないファイルは無視する。
// TightenSharedConfigMode rewrites the mode of path to owner-only (0600) while
// keeping a deliberately granted group-read bit (0640) intact.
//
// Why this exists (A-4): the agent runs as its own system user (kizuna-eye)
// while the dashboard — and therefore the operator who owns the data directory
// — keeps running as the login account. The agent has to read the shared
// configuration (agent_config.json, modules.json) that the dashboard rewrites on
// every save. A blanket chmod 0600 on load would make those files unreadable for
// the agent as soon as the dashboard touched them again, and would silently undo
// the group bit the migration step sets. Preserving "group read only" keeps the
// shared-read contract while still closing everything wider:
//
//	0644, 0666, 0660   -> 0640
//	0600, 0640, 0700   -> unchanged (0640) / 0600
//	0777               -> 0640
//	0400, 0000         -> 0600
//
// Best-effort, like the calls it replaces: filesystems that do not honour chmod
// (some SMB mounts) and files that cannot be stat'ed are ignored.
func TightenSharedConfigMode(path string) {
	if path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	perm := info.Mode().Perm()
	mode := os.FileMode(0600)
	if perm&0o040 != 0 {
		// 所有者が意図的にグループ読み取りを与えている（移行が共有設定ファイルに
		// 対して行う）。それを保ち、他はすべて落とす。
		// The owner deliberately granted group read (the migration does this
		// for the shared config files). Keep it, drop everything else.
		mode = 0640
	}
	if perm == mode {
		return
	}
	_ = os.Chmod(path, mode)
}

// SharedFileMode は、dashboard が書き agent が読む可能性のあるファイルを
// 作成・アトミック書き換えする際のモードを返す。現状ではアラート履歴
// （logs/alert_history.jsonl）が該当し、security プラグインが V2-B 整合性
// チェックで読む一方、dashboard が追記する。
//
// なぜ書き込みの「前」にモードを決める必要があるか: os.OpenFile(O_CREATE) も
// WriteFileAtomic（一時ファイル + rename）も作成時にモードを設定するため、
// 「書いてから stat してグループビットを保つ」は不可能だ。書き換えのたびに既存の
// 0640 が 0600 に戻り、agent が "open failed: permission denied" で失敗する。
// プラグインはそれに警告を出すので、A-4 配備ではチェック間隔ごとに
// alert_history_tamper を報告してしまう。
//
// 規則（TightenSharedConfigMode と対になる）:
//   - path が存在: グループ読み取りが既に与えられていれば 0640、なければ 0600。
//   - path が無い: 親ディレクトリがグループ書き込み可能なら 0640。A-4 の共有ログ
//     ディレクトリ（3770）はまさにそれで、History.Clear() 後に再作成されたファイルは
//     再び共有成果物になる。そうでなければ fallback。
//
// fallback は何も stat できない場合に使う（呼び出し側は 0600 を渡す）。
// SharedFileMode returns the mode to use when creating or atomically rewriting a
// file that the dashboard writes and the agent may have to read. Today that is
// the alert history (logs/alert_history.jsonl), which the security plugin reads
// for its V2-B consistency check while the dashboard appends to it.
//
// Why the mode has to be decided *before* the write: os.OpenFile(O_CREATE) and
// WriteFileAtomic (temp file + rename) both set the mode as the file is created,
// so "write, then stat and keep the group bit" is not possible — a rewrite would
// drop an existing 0640 back to 0600, and the agent would fail with
// "open failed: permission denied". The plugin raises a warning for that, so an
// A-4 deployment would report alert_history_tamper every check interval.
//
// Rules (mirroring TightenSharedConfigMode):
//   - path exists: 0640 when group read is already granted, otherwise 0600.
//   - path missing: 0640 when the parent directory is group-writable — the A-4
//     shared log directory (3770) is exactly that, so a file recreated after
//     History.Clear() is a shared artifact again; otherwise fallback.
//
// fallback is used when nothing can be stat'ed (callers pass 0600).
func SharedFileMode(path string, fallback os.FileMode) os.FileMode {
	if path == "" {
		return fallback
	}
	if info, err := os.Stat(path); err == nil {
		if info.Mode().Perm()&0o040 != 0 {
			return 0640
		}
		return 0600
	}
	if info, err := os.Stat(filepath.Dir(path)); err == nil && info.Mode().Perm()&0o020 != 0 {
		return 0640
	}
	return fallback
}
