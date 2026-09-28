package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestModulesStorageCRUD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modules.json")
	s := NewModulesStorage(path)

	// Starts empty.
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.GetAll()) != 0 {
		t.Fatalf("expected empty, got %d", len(s.GetAll()))
	}

	// Add
	if err := s.Add(ModuleConfig{Name: "p1", Type: "plugin", Enabled: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(ModuleConfig{Name: "p1", Type: "plugin"}); err == nil {
		t.Fatal("duplicate Add should fail")
	}
	if err := s.Add(ModuleConfig{Name: ""}); err == nil {
		t.Fatal("empty name Add should fail")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify persistence after reload.
	s2 := NewModulesStorage(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load2: %v", err)
	}
	if got := s2.Get("p1"); got == nil || !got.Enabled {
		t.Fatalf("p1 not persisted correctly: %#v", got)
	}

	// Update
	if err := s2.Update("p1", ModuleConfig{Name: "p1", Type: "plugin", Enabled: false}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s2.Get("p1"); got == nil || got.Enabled {
		t.Fatalf("p1 not updated: %#v", got)
	}
	if err := s2.Update("nope", ModuleConfig{Name: "nope"}); err == nil {
		t.Fatal("Update of missing should fail")
	}

	// Delete
	if err := s2.Delete("p1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s2.Get("p1") != nil {
		t.Fatal("p1 should be deleted")
	}
	if err := s2.Delete("p1"); err == nil {
		t.Fatal("Delete of missing should fail")
	}
}

func TestModulesStorageSavePreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modules.json")
	s := NewModulesStorage(path)
	_ = s.Add(ModuleConfig{Name: "p1", Type: "plugin", Enabled: true})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Tighten the permission, then save again. The mode must be preserved,
	// not reset to 0644.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Skipf("chmod unsupported on this filesystem: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		// e.g. an SMB share with a forced create mask; chmod has no effect here.
		t.Skip("filesystem does not honour chmod; skipping mode-preservation check")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save2: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("Save should preserve 0600, got %o", perm)
	}
}

func TestModulesStorageSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modules.json")
	s := NewModulesStorage(path)
	_ = s.Add(ModuleConfig{Name: "a", Type: "plugin", Enabled: true})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No leftover temp files must remain after a successful save.
	matches, _ := filepath.Glob(path + ".tmp-*")
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}

	// The file must be valid JSON (never half-written).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var out []ModuleConfig
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
}

func TestModulesStorageGetReturnsCopy(t *testing.T) {
	s := NewModulesStorage(filepath.Join(t.TempDir(), "m.json"))
	_ = s.Add(ModuleConfig{Name: "a", Type: "plugin", Enabled: true})

	got := s.Get("a")
	got.Enabled = false // mutating the copy must not affect storage
	if !s.Get("a").Enabled {
		t.Error("Get should return a copy, not a shared reference")
	}
}

// When the save fails, the mutation must be rolled back so the in-memory
// list never diverges from the on-disk file.
func TestModulesStorageAddRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	// Point the storage at a path whose parent is a *file*, not a directory,
	// so saveLocked fails (MkdirAll / CreateTemp cannot proceed).
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "modules.json")

	s := NewModulesStorage(path)
	if err := s.Add(ModuleConfig{Name: "p1", Type: "plugin", Enabled: true}); err == nil {
		t.Fatal("Add should fail when the file cannot be saved")
	}
	if got := s.Get("p1"); got != nil {
		t.Fatalf("in-memory list should be rolled back, got %#v", got)
	}
	if len(s.GetAll()) != 0 {
		t.Fatalf("GetAll should be empty after rollback, got %d", len(s.GetAll()))
	}
}

func TestModulesStorageUpdateRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modules.json")
	s := NewModulesStorage(path)
	if err := s.Add(ModuleConfig{Name: "p1", Type: "plugin", Enabled: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Make subsequent saves fail by replacing the file path with a directory.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.Update("p1", ModuleConfig{Name: "p1", Type: "plugin", Enabled: false}); err == nil {
		t.Fatal("Update should fail when the file cannot be saved")
	}
	if got := s.Get("p1"); got == nil || !got.Enabled {
		t.Fatalf("in-memory entry should keep the previous value, got %#v", got)
	}
}

func TestModulesStorageDeleteRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modules.json")
	s := NewModulesStorage(path)
	if err := s.Add(ModuleConfig{Name: "p1", Type: "plugin", Enabled: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Make subsequent saves fail.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete("p1"); err == nil {
		t.Fatal("Delete should fail when the file cannot be saved")
	}
	if got := s.Get("p1"); got == nil {
		t.Fatalf("deleted entry should be restored on save failure, got %#v", got)
	}
	if len(s.GetAll()) != 1 {
		t.Fatalf("GetAll should still contain 1 entry, got %d", len(s.GetAll()))
	}
}
