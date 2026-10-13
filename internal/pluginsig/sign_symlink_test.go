package pluginsig

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSignFileRejectsSymlinkTarget pins the CWE-59 fix: SignFile must not
// follow a symlink planted at the signature path. Before the fix it used
// os.WriteFile, which happily wrote through the link and let the caller pick
// the real destination.
func TestSignFileRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	soPath := filepath.Join(dir, "Foo.so")
	if err := os.WriteFile(soPath, []byte("not really a plugin"), 0o644); err != nil {
		t.Fatalf("write .so: %v", err)
	}

	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	sigPath := SigPath(soPath)
	// Symlinks need privileges on some platforms; skip rather than fail.
	if err := os.Symlink(victim, sigPath); err != nil {
		t.Skipf("symlink unavailable on this platform: %v", err)
	}

	_, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	if err := SignFile(soPath, sigPath, priv); err == nil {
		t.Fatal("SignFile accepted a symlink target; it must refuse")
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("victim file was overwritten through the symlink: %q", got)
	}
}
