package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadCronDirDirectHashesFiles verifies the A-4 direct-read path lists a
// readable directory and hashes each regular file (path-unsafe names skipped).
func TestReadCronDirDirectHashesFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user"), []byte("a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0700); err != nil {
		t.Fatal(err)
	}

	got, err := readCronDirDirect(dir)
	if err != nil {
		t.Fatalf("readCronDirDirect: %v", err)
	}
	if _, ok := got["user"]; !ok {
		t.Errorf("expected 'user' in %v", got)
	}
	if _, ok := got["subdir"]; ok {
		t.Errorf("subdir must be skipped")
	}
}

// TestReadCronDirDirectMissingDir verifies a missing/unreadable directory
// returns an error so the caller falls back.
func TestReadCronDirDirectMissingDir(t *testing.T) {
	if _, err := readCronDirDirect(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing dir")
	}
}
