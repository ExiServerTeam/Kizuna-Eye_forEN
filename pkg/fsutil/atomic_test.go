package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomicCreatesWithMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "state.json")
	if err := WriteFileAtomic(path, []byte("hello\n"), 0600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("content = %q, want %q", got, "hello\n")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("perm = %04o, want 0600", perm)
		}
	}
	// No temporary files may be left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Errorf("leftover files: %v", entries)
	}
}

func TestWriteFileAtomicReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}
}

func TestWriteFileAtomicRejectsEmptyPath(t *testing.T) {
	if err := WriteFileAtomic("", []byte("x"), 0600); err == nil {
		t.Fatal("empty path must fail")
	}
}

func TestWriteFileAtomicRejectsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not available to unprivileged Windows users")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	if err := os.WriteFile(target, []byte("real"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := WriteFileAtomic(link, []byte("attacker"), 0600); err == nil {
		t.Fatal("WriteFileAtomic must refuse to write through a symlink")
	}
	// The symlink target must be untouched.
	got, _ := os.ReadFile(target)
	if string(got) != "real" {
		t.Errorf("target content = %q, want real", got)
	}
}
