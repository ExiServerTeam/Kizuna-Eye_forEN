package alert

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// alertHistorySigField is the JSON key that carries the detached HMAC of a
// history line. It is excluded from the signed payload so the signature can
// be computed over the rest of the line.
const alertHistorySigField = "sig"

// ensureGroupReadable best-effort chmods path to 0640 so the agent user (group
// kizuna-eye) can read a signing key the dashboard owns. Failures are ignored:
// a filesystem that does not honour chmod (some SMB mounts) or a file owned by
// another user cannot be adjusted here, and the root migration step is
// responsible for the authoritative mode.
func ensureGroupReadable(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.Mode().Perm() == 0640 {
		return
	}
	_ = os.Chmod(path, 0640)
}

// LoadOrCreateAlertHistoryKey reads an HMAC key for alert-history signing.
//
// It uses a key dedicated to this purpose (not the security-log chain key):
// keeping one key per use means a leak or rotation of one does not disable
// the other's tamper detection.
//
// Permissions (H-1 / A-4): the agent runs as its own user (kizuna-eye) and
// must be able to READ this key to verify alert-history signatures, while the
// dashboard (the writer) runs as the operator user. The key is therefore
// created 0640 (group read) inside a 0750 directory (group traverse). The
// owner/group (user:kizuna-eye) and the setgid bit on the directory are set by
// systemd/migrate-agent-user.sh, which runs as root; this function only sets
// the modes (it may run as the non-root dashboard user).
//
// An empty path returns (nil, nil): signing is then disabled and lines are
// written unsigned (fully backward compatible).
func LoadOrCreateAlertHistoryKey(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) == 0 {
			return nil, fmt.Errorf("alert history key is empty: %s", path)
		}
		// Best-effort: grant group read so the agent can verify signatures.
		// (The owner/group themselves are set by the root migration step.)
		ensureGroupReadable(path)
		return data, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// 0750: owner rwx, group r-x (traverse), other none. The setgid bit is
	// applied by the root migration step so new files inherit the group.
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	// O_EXCL makes creation fail if the path already exists (including a
	// symlink), so a pre-planted symlink cannot redirect the key write.
	// 0640: group read for the agent (see the doc comment).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		if os.IsExist(err) {
			return os.ReadFile(path)
		}
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

// signHistoryEntry computes the HMAC of an entry with the "sig" field removed.
// It returns "" when key is empty (signing disabled).
func signHistoryEntry(key []byte, e HistoryEntry) (string, error) {
	if len(key) == 0 {
		return "", nil
	}
	m, err := entryToMap(e)
	if err != nil {
		return "", err
	}
	delete(m, alertHistorySigField)
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyHistoryLineSig reports whether the line's "sig" matches its content.
//
// It returns (true, nil) when the line has no signature (legacy line: not
// verified, so no false positive) or when the signature is valid, and
// (false, nil) when a present signature does not match.
func VerifyHistoryLineSig(key []byte, line []byte) (bool, error) {
	if len(key) == 0 {
		return true, nil
	}
	var e HistoryEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return false, err
	}
	got := e.Sig
	if got == "" {
		return true, nil // legacy unsigned line: skip
	}
	want, err := signHistoryEntry(key, e)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(got), []byte(want)), nil
}

// entryToMap converts a HistoryEntry to a map so the signature can be computed
// over exactly the fields that are serialized (the struct tags define the wire
// form; a map round-trip keeps the field set in sync with the struct).
func entryToMap(e HistoryEntry) (map[string]interface{}, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
