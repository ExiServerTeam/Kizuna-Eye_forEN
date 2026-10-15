package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"Kizuna-Eye/pkg/fsutil"
)

// users.json の完全性検査。users.json は毎回まるごと書き換わるため、追記専用
// ログのようなハッシュチェーンは使えない。代わりにファイル全体に対する
// HMAC-SHA256 をファイル内に埋め込み、読み込み時に検証する（改ざん検知）。
// Integrity for users.json. The file is rewritten wholesale on every change,
// so an append-only hash chain does not apply; instead we embed an HMAC-SHA256
// over the whole canonical users list and verify it on load (tamper detection).

// usersKeySize は users.json の完全性検査に使う HMAC 鍵の長さ（バイト）。
// usersKeySize is the length (bytes) of the HMAC key for users.json integrity.
const usersKeySize = 32

// usersMACField は users.json に埋め込む MAC フィールド名。
// usersMACField is the MAC field name embedded in users.json.
const usersMACField = "mac"

// ErrUsersMACMismatch は users.json の MAC が一致しないとき返す。
// ErrUsersMACMismatch is returned when the users.json MAC does not match.
var ErrUsersMACMismatch = errors.New("users.json の改ざんを検知しました（MAC 不一致）")

// canonicalUsers は users を決定的なバイト列へ直列化する（ユーザー名順・コンパクト）。
// 保存時と検証時で同じ手順を使うことで、整形の揺れで MAC が変わらないようにする。
// canonicalUsers serializes users deterministically (sorted by username,
// compact) so the same bytes are produced at save and verify time.
func canonicalUsers(users map[string]*User) ([]byte, error) {
	list := make([]*User, 0, len(users))
	for _, u := range users {
		if u != nil && u.Username != "" {
			list = append(list, u)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Username < list[j].Username })
	return json.Marshal(list)
}

// computeUsersMAC は canonical に対する HMAC-SHA256 を base64 で返す。
// computeUsersMAC returns base64(HMAC-SHA256(key, canonical)).
func computeUsersMAC(key, canonical []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(canonical)
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// LoadOrCreateUsersKey は keyPath の HMAC 鍵を読み込み、無ければ 32B を生成して
// 0600 で保存する。親ディレクトリは 0700 で作る。
// LoadOrCreateUsersKey reads the HMAC key at keyPath, or generates 32 bytes and
// stores them 0600. The parent directory is created 0700.
func LoadOrCreateUsersKey(keyPath string) ([]byte, error) {
	if keyPath == "" {
		return nil, fmt.Errorf("users 鍵のパスが空です")
	}
	if data, err := os.ReadFile(keyPath); err == nil && len(data) >= usersKeySize {
		return data[:usersKeySize], nil
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, fmt.Errorf("鍵ディレクトリの作成に失敗: %w", err)
	}
	key := make([]byte, usersKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("鍵の生成に失敗: %w", err)
	}
	if err := fsutil.WriteFileAtomic(keyPath, key, 0600); err != nil {
		return nil, fmt.Errorf("鍵の保存に失敗: %w", err)
	}
	return key, nil
}
