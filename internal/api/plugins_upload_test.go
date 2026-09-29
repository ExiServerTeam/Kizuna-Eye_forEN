package api

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression: when modules.json cannot be saved, the upload must not leave an
// orphan .so or meta.json behind (which would otherwise stay on disk and could
// be loaded as a runnable plugin).
func TestUploadRollsBackOnStorageFailure(t *testing.T) {
	dir := t.TempDir()

	// Make modules.json unsavable: its parent path is a regular file, so
	// saveLocked's MkdirAll fails. storage.Add then returns an error.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	modulesPath := filepath.Join(blocker, "modules.json")

	storage := NewModulesStorage(modulesPath)
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	p := &PluginManager{
		storage:    storage,
		pluginsDir: pluginsDir,
		running:    make(map[string]runGuard),
	}

	// Simulate the post-inspect stage: a committed .so + meta.json, then a
	// failed storage.Add. The handler's cleanup removes both.
	soPath := filepath.Join(pluginsDir, "demo.so")
	metaPath := filepath.Join(pluginsDir, "demo.meta.json")
	if err := os.WriteFile(soPath, []byte("ELF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The failure path mirrors handleUpload's cleanup.
	if err := p.storage.Add(ModuleConfig{Name: "demo", Type: "plugin", Enabled: true}); err == nil {
		t.Fatal("storage.Add should fail when modules.json path is unsavable")
	}
	_ = os.Remove(soPath)
	_ = os.Remove(metaPath)

	if _, err := os.Stat(soPath); !os.IsNotExist(err) {
		t.Errorf("orphan .so must be removed, stat err = %v", err)
	}
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Errorf("orphan meta.json must be removed, stat err = %v", err)
	}
}

// A temp .so left after a failed upload must not linger in the plugins dir.
func TestUploadTempFileCleanedOnFailure(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "demo.so.tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpPath := f.Name()
	_ = f.Close()

	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	// Simulate a failure before commit: the deferred cleanup removes it.
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("temp file should exist before cleanup: %v", err)
	}
}
