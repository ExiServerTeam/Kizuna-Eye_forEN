package api

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReconcileWebUIDirRename verifies that a plugin whose uploaded file name
// differs from its own Name() still has its web UI served: the extracted
// <upload>.web directory must be moved to <canonical>.web, which is what
// handleWebUI and handleList look up.
func TestReconcileWebUIDirRename(t *testing.T) {
	dir := t.TempDir()
	p := &PluginManager{pluginsDir: dir}

	src := filepath.Join(dir, "uploaded_name.web")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("<html>x</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	p.reconcileWebUIDir("uploaded_name", "canonical_name")

	dst := filepath.Join(dir, "canonical_name.web")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("expected the web UI at %s: %v", dst, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("the upload-name dir should be gone, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "index.html")); err != nil {
		t.Fatalf("web UI contents must move with the directory: %v", err)
	}
}

// No web UI was extracted: the rename must be a no-op (no error, no dir).
func TestReconcileWebUIDirMissingSrc(t *testing.T) {
	dir := t.TempDir()
	p := &PluginManager{pluginsDir: dir}

	p.reconcileWebUIDir("nope", "canonical")

	if _, err := os.Stat(filepath.Join(dir, "canonical.web")); !os.IsNotExist(err) {
		t.Fatalf("no directory should be created when there is no web UI")
	}
}

// A pre-existing canonical directory must be preserved (not overwritten).
func TestReconcileWebUIDirKeepsExistingDst(t *testing.T) {
	dir := t.TempDir()
	p := &PluginManager{pluginsDir: dir}

	src := filepath.Join(dir, "uploaded.web")
	dst := filepath.Join(dir, "canonical.web")
	for _, d := range []string{src, dst} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dst, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	p.reconcileWebUIDir("uploaded", "canonical")

	if _, err := os.Stat(filepath.Join(dst, "keep.txt")); err != nil {
		t.Fatalf("existing canonical dir must be preserved: %v", err)
	}
}
