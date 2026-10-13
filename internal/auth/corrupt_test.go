package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCorruptStoreDoesNotNeedSetup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	if err := os.WriteFile(path, []byte("not valid json{{{"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := NewStore(path)
	if err := s.Load(); err == nil {
		t.Fatal("Load should return an error for corrupt JSON")
	}
	if !s.IsCorrupt() {
		t.Fatal("store should be marked corrupt")
	}
	if s.NeedsSetup() {
		t.Fatal("a corrupt store must NOT report NeedsSetup (privilege escalation)")
	}
}

func TestCorruptStoreRejectsCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := NewStore(path)
	_ = s.Load()
	if _, err := s.Create("admin", "password123", RoleAdmin); err != ErrStoreCorrupt {
		t.Fatalf("Create on corrupt store = %v, want ErrStoreCorrupt", err)
	}
}

func TestMissingFileNeedsSetup(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "missing.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.IsCorrupt() {
		t.Fatal("missing file must not be corrupt")
	}
	if !s.NeedsSetup() {
		t.Fatal("missing file should need setup")
	}
}
