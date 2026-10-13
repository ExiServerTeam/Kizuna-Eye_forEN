package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// newUpdaterWithScript builds an Updater whose UpdateScript points at path.
func newUpdaterWithScript(path string) *Updater {
	return &Updater{cfg: Config{UpdateScript: path}}
}

// The permission checks are POSIX-only: Windows does not store the exec bit
// and reports every regular file as 0666, so a mode-based assertion is
// meaningless there (the production target is Linux).
func posixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
}

// TestVerifyUpdateScriptRejectsGroupWritable pins the supply-chain check: the
// update script runs with the agent's privileges, so a script that a
// non-privileged writer can change (group/other write) must be refused. This
// is the concrete risk of keeping the repo on a shared Samba folder.
func TestVerifyUpdateScriptRejectsGroupWritable(t *testing.T) {
	posixOnly(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "safe_update.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Chmod explicitly: os.WriteFile applies mode &^ umask, so with a umask
	// of 022 a 0o775 create yields 0o755 (not group-writable) and the test
	// would pass trivially (or fail on a different umask). Forcing the mode
	// makes the assertion independent of the ambient umask.
	if err := os.Chmod(p, 0o775); err != nil { // group-writable
		t.Fatal(err)
	}
	if _, err := newUpdaterWithScript(p).verifyUpdateScript(); err == nil {
		t.Fatal("group-writable update script must be refused")
	}

	if err := os.Chmod(p, 0o666); err != nil { // other-writable
		t.Fatal(err)
	}
	if _, err := newUpdaterWithScript(p).verifyUpdateScript(); err == nil {
		t.Fatal("other-writable update script must be refused")
	}
}

// A well-owned (0755) regular file is accepted.
func TestVerifyUpdateScriptAcceptsOwnerOnly(t *testing.T) {
	posixOnly(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "safe_update.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := newUpdaterWithScript(p).verifyUpdateScript()
	if err != nil {
		t.Fatalf("0755 script must be accepted: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("expected an absolute path, got %q", got)
	}
}

// A symlink is refused even when it points at a safe file (the target could
// be swapped later).
func TestVerifyUpdateScriptRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.sh")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "safe_update.sh")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := newUpdaterWithScript(link).verifyUpdateScript(); err == nil {
		t.Fatal("symlink update script must be refused")
	}
}

// A missing script is an error, not a silent no-op.
func TestVerifyUpdateScriptMissing(t *testing.T) {
	if _, err := newUpdaterWithScript(filepath.Join(t.TempDir(), "nope.sh")).verifyUpdateScript(); err == nil {
		t.Fatal("missing update script must be an error")
	}
}
