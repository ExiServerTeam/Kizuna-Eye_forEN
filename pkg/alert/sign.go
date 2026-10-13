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

// alertHistorySigField は履歴行の分離 HMAC を載せる JSON キーだ。
// 署名をその行の残りに対して計算できるよう、署名対象ペイロードから除外する。
// alertHistorySigField is the JSON key that carries the detached HMAC of a
// history line. It is excluded from the signed payload so the signature can
// be computed over the rest of the line.
const alertHistorySigField = "sig"

// ensureGroupReadable は best-effort で path を 0640 に chmod し、agent ユーザー
// （グループ kizuna-eye）が dashboard 所有の署名鍵を読めるようにする。失敗は無視する。
// chmod を尊重しないファイルシステム（一部の SMB マウント）や他ユーザー所有の
// ファイルはここでは調整できず、権威あるモード設定は root の移行手順が担う。
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

// LoadOrCreateAlertHistoryKey はアラート履歴署名用の HMAC 鍵を読む。
//
// この用途専用の鍵を使う（セキュリティログのチェーン鍵ではない）。
// 用途ごとに鍵を分けることで、片方の漏洩やローテーションがもう片方の
// 改ざん検知を無効化しない。
//
// 権限（H-1 / A-4）: agent は専用ユーザー（kizuna-eye）で動き、アラート履歴署名を
// 検証するためにこの鍵を「読める」必要がある。一方 dashboard（書き手）は
// 運用ユーザーで動く。そのため鍵は 0750 ディレクトリ（グループ通過）内に
// 0640（グループ読み取り）で作成する。所有者/グループ（user:kizuna-eye）と
// ディレクトリの setgid ビットは root で動く systemd/migrate-agent-user.sh が設定する。
// この関数はモードだけを設定する（非 root の dashboard ユーザーで動き得る）。
//
// 空の path は (nil, nil) を返す。その場合署名は無効化され、行は未署名で
// 書かれる（完全に後方互換）。
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
		// ベストエフォート: agent が署名を検証できるようグループ読み取りを与える。
		// （所有者/グループ自体は root の移行手順が設定する。）
		// Best-effort: grant group read so the agent can verify signatures.
		// (The owner/group themselves are set by the root migration step.)
		ensureGroupReadable(path)
		return data, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// 0750: 所有者 rwx、グループ r-x（通過）、その他なし。setgid ビットは
	// root の移行手順が適用し、新規ファイルがグループを継承する。
	// 0750: owner rwx, group r-x (traverse), other none. The setgid bit is
	// applied by the root migration step so new files inherit the group.
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	// O_EXCL により、パスが既に存在する場合（シンボリックリンク含む）は作成が失敗する。
	// これにより先に仕込まれたシンボリックリンクが鍵の書き込み先を誘導できない。
	// 0640: agent 用のグループ読み取り（doc コメント参照）。
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

// signHistoryEntry は "sig" フィールドを除いたエントリの HMAC を計算する。
// key が空（署名無効）の場合は "" を返す。
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

// VerifyHistoryLineSig は行の "sig" が内容と一致するかを返す。
//
// 行に署名が無い場合（legacy 行: 検証せず、誤検知しない）または署名が有効な場合は
// (true, nil) を、署名が存在して一致しない場合は (false, nil) を返す。
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
		return true, nil // 未署名の legacy 行: スキップ / legacy unsigned line: skip
	}
	want, err := signHistoryEntry(key, e)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(got), []byte(want)), nil
}

// entryToMap は HistoryEntry を map へ変換し、シリアライズされるフィールドに
// 正確にわたって署名を計算できるようにする（struct タグがワイヤ形式を定義し、
// map 往復でフィールド集合が struct と同期する）。
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
