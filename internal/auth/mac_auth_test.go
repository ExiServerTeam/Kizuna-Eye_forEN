package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// CRITICAL regression: a users.json whose MAC does not match must never
// authenticate anyone, even the injected (backdoor) account.
func TestTamperedUsersCannotAuthenticate(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.json")
	keyPath := filepath.Join(dir, "keys", "users.key")

	s := NewStore(usersPath)
	if err := s.EnableIntegrity(keyPath); err != nil {
		t.Fatalf("EnableIntegrity: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := s.Create("admin", "adminpassword", RoleAdmin); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// ファイルを読み、backdoor を追加し、MAC を更新せずに書き戻す（改ざん）。
	// Read the file, add a backdoor, write it back WITHOUT updating the MAC.
	raw, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("backdoorpass"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	users := doc["users"].([]interface{})
	users = append(users, map[string]interface{}{
		"username":      "backdoor",
		"password_hash": string(hash),
		"role":          "admin",
		"created_at":    "2026-01-01T00:00:00Z",
	})
	doc["users"] = users
	out, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(usersPath, out, 0600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}

	// 再ロードは失敗し、破損扱いになる。
	// Reload fails and is marked corrupt.
	s2 := NewStore(usersPath)
	if err := s2.EnableIntegrity(keyPath); err != nil {
		t.Fatalf("EnableIntegrity(2): %v", err)
	}
	if err := s2.Load(); err == nil {
		t.Fatal("tampered users.json must fail to load")
	}
	if !s2.IsCorrupt() {
		t.Fatal("tampered store must be marked corrupt")
	}

	// 改ざんで追加した backdoor でも、既存 admin でも、認証は通ってはならない。
	// Neither the injected backdoor nor the real admin may authenticate.
	if _, ok := s2.Authenticate("backdoor", "backdoorpass"); ok {
		t.Fatal("backdoor account must NOT authenticate on a tampered store")
	}
	if _, ok := s2.Authenticate("admin", "adminpassword"); ok {
		t.Fatal("no account may authenticate while the store is corrupt")
	}
}
