package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveAndCheckPluginPath verifies that only .so files inside the
// allowed plugins directory are accepted, so a tampered modules.json cannot
// load an arbitrary library.
func TestResolveAndCheckPluginPath(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(pluginsDir, "ok.so")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "evil.so")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Inside the plugins dir is accepted.
	got, err := resolveAndCheckPluginPath(inside, pluginsDir)
	if err != nil {
		t.Fatalf("inside path should be accepted: %v", err)
	}
	if got == "" {
		t.Fatal("inside path returned empty")
	}

	// Outside the plugins dir is rejected.
	if _, err := resolveAndCheckPluginPath(outside, pluginsDir); err == nil {
		t.Fatal("outside path should be rejected")
	}

	// A traversal attempt is rejected.
	traversal := filepath.Join(pluginsDir, "..", "evil.so")
	if _, err := resolveAndCheckPluginPath(traversal, pluginsDir); err == nil {
		t.Fatal("traversal path should be rejected")
	}

	// A non-.so file inside the dir is rejected.
	txt := filepath.Join(pluginsDir, "note.txt")
	if err := os.WriteFile(txt, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAndCheckPluginPath(txt, pluginsDir); err == nil {
		t.Fatal("non-.so path should be rejected")
	}

	// A symlink inside the dir pointing outside is rejected.
	link := filepath.Join(pluginsDir, "link.so")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := resolveAndCheckPluginPath(link, pluginsDir); err == nil {
			t.Fatal("symlink escaping the plugins dir should be rejected")
		}
	}
}
