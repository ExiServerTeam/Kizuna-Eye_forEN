package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// users.json の MAC 完全性: 保存→再読込で一致、改ざんで不一致、旧形式は unsigned。
// users.json MAC integrity: save->reload matches, tamper mismatches, legacy is unsigned.
func TestUsersMACRoundTripAndTamper(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.json")
	keyPath := filepath.Join(dir, "keys", "users.key")

	s := NewStore(usersPath)
	if err := s.EnableIntegrity(keyPath); err != nil {
		t.Fatalf("EnableIntegrity: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load (empty): %v", err)
	}
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 保存されたファイルには mac が入っている。
	// The saved file carries a mac.
	data, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !containsField(data, "\"mac\"") {
		t.Fatalf("users.json should contain a mac field: %s", data)
	}

	// 同じ鍵で読み直すと検証を通る。
	// Reloading with the same key verifies.
	s2 := NewStore(usersPath)
	if err := s2.EnableIntegrity(keyPath); err != nil {
		t.Fatalf("EnableIntegrity(2): %v", err)
	}
	if err := s2.Load(); err != nil {
		t.Fatalf("Load (signed) should pass: %v", err)
	}
	if s2.IsCorrupt() {
		t.Fatal("signed store must not be corrupt")
	}

	// 改ざん（ロールを書き換える）→ ロードが失敗し corrupt になる。
	// Tamper (flip the role) -> load fails and marks corrupt.
	tampered := replaceFirst(string(data), `"role": "admin"`, `"role": "viewer"`)
	if err := os.WriteFile(usersPath, []byte(tampered), 0600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	s3 := NewStore(usersPath)
	if err := s3.EnableIntegrity(keyPath); err != nil {
		t.Fatalf("EnableIntegrity(3): %v", err)
	}
	if err := s3.Load(); err == nil {
		t.Fatal("tampered users.json must fail to load")
	}
	if !s3.IsCorrupt() {
		t.Fatal("tampered store must be marked corrupt")
	}
}

// MAC の無い旧形式は unsigned として読み込め、次の保存で署名される。
// A legacy file with no MAC loads as unsigned and is signed on the next save.
func TestUsersLegacyUnsignedThenSigned(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.json")
	keyPath := filepath.Join(dir, "keys", "users.key")

	legacy := `{"users":[{"username":"admin","password_hash":"x","role":"admin","created_at":"2026-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(usersPath, []byte(legacy), 0600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	s := NewStore(usersPath)
	if err := s.EnableIntegrity(keyPath); err != nil {
		t.Fatalf("EnableIntegrity: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("legacy load should pass: %v", err)
	}
	if !s.IsUnsigned() {
		t.Fatal("legacy store should be unsigned")
	}
	if s.IsCorrupt() {
		t.Fatal("legacy store must not be corrupt")
	}

	// 変更して保存すると mac が付く（ChangePassword は last-admin 制約が無い）。
	// A mutation signs the file (ChangePassword has no last-admin constraint).
	if err := s.ChangePassword("admin", "newpassword123"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	data, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !containsField(data, "\"mac\"") {
		t.Fatalf("saved file should now carry a mac: %s", data)
	}
}

// 別の鍵で読むと検証に失敗する。
// Reading with a different key fails verification.
func TestUsersMACWrongKeyFails(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.json")
	keyA := filepath.Join(dir, "a.key")
	keyB := filepath.Join(dir, "b.key")

	s := NewStore(usersPath)
	if err := s.EnableIntegrity(keyA); err != nil {
		t.Fatalf("EnableIntegrity(a): %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s.Create("admin", "password123", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	s2 := NewStore(usersPath)
	if err := s2.EnableIntegrity(keyB); err != nil {
		t.Fatalf("EnableIntegrity(b): %v", err)
	}
	if err := s2.Load(); err == nil {
		t.Fatal("loading with the wrong key must fail")
	}
}

// 壊れた（短すぎる）鍵は黙って作り直さず、明示エラーになる。
// A too-short (corrupt) key must not be silently regenerated; it errors.
func TestUsersShortKeyIsNotRegenerated(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "users.key")
	if err := os.WriteFile(keyPath, []byte("tooshort"), 0600); err != nil {
		t.Fatalf("write short key: %v", err)
	}
	before, _ := os.ReadFile(keyPath)

	if _, err := LoadOrCreateUsersKey(keyPath); err == nil {
		t.Fatal("a too-short key must return an error, not be regenerated")
	}
	after, _ := os.ReadFile(keyPath)
	if string(before) != string(after) {
		t.Fatal("the corrupt key file must be left untouched")
	}
}

// 新規（存在しない）鍵だけが生成される。
// Only a missing key is generated.
func TestUsersMissingKeyIsGenerated(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "keys", "users.key")
	k, err := LoadOrCreateUsersKey(keyPath)
	if err != nil {
		t.Fatalf("missing key should be generated: %v", err)
	}
	if len(k) != usersKeySize {
		t.Fatalf("generated key length = %d, want %d", len(k), usersKeySize)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("generated key should be written: %v", err)
	}
}

func containsField(b []byte, field string) bool {
	return stringIndex(string(b), field) >= 0
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func replaceFirst(s, old, new string) string {
	i := stringIndex(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}
