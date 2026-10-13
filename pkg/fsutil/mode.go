package fsutil

import (
	"os"
	"path/filepath"
)

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
		// The owner deliberately granted group read (the migration does this
		// for the shared config files). Keep it, drop everything else.
		mode = 0640
	}
	if perm == mode {
		return
	}
	_ = os.Chmod(path, mode)
}

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
