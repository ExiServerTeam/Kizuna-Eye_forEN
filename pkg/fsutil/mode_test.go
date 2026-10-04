package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTightenSharedConfigMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	cases := []struct {
		in   os.FileMode
		want os.FileMode
	}{
		{0600, 0600},
		{0640, 0640}, // deliberate group read is preserved
		{0644, 0640},
		{0660, 0640},
		{0666, 0640},
		{0777, 0640},
		{0700, 0600},
		{0400, 0600},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "modules.json")
		if err := os.WriteFile(path, []byte("[]"), tc.in); err != nil {
			t.Fatalf("WriteFile(%04o): %v", tc.in, err)
		}
		if err := os.Chmod(path, tc.in); err != nil {
			t.Fatalf("Chmod(%04o): %v", tc.in, err)
		}

		TightenSharedConfigMode(path)

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if got := info.Mode().Perm(); got != tc.want {
			t.Errorf("mode %04o: got %04o, want %04o", tc.in, got, tc.want)
		}
	}
}

func TestTightenSharedConfigModeMissingFile(t *testing.T) {
	// Must not panic or create anything when the file is gone.
	TightenSharedConfigMode(filepath.Join(t.TempDir(), "does-not-exist.json"))
	TightenSharedConfigMode("")
}

// SharedFileMode decides the mode *before* a write (O_CREATE / atomic rewrite),
// which is what keeps the A-4 group read alive on alert_history.jsonl.
func TestSharedFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	// Existing file: same policy as TightenSharedConfigMode.
	cases := []struct {
		in   os.FileMode
		want os.FileMode
	}{
		{0600, 0600},
		{0640, 0640}, // deliberate group read is preserved
		{0644, 0640},
		{0666, 0640},
		{0777, 0640},
		{0700, 0600},
		{0400, 0600},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "alert_history.jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), tc.in); err != nil {
			t.Fatalf("WriteFile(%04o): %v", tc.in, err)
		}
		if err := os.Chmod(path, tc.in); err != nil {
			t.Fatalf("Chmod(%04o): %v", tc.in, err)
		}
		if got := SharedFileMode(path, 0600); got != tc.want {
			t.Errorf("existing %04o: got %04o, want %04o", tc.in, got, tc.want)
		}
	}

	// Missing file: a group-writable directory is the A-4 shared log directory
	// (3770), where a recreated file must stay readable for the agent.
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o770); err != nil {
		t.Fatalf("chmod shared dir: %v", err)
	}
	if got := SharedFileMode(filepath.Join(shared, "alert_history.jsonl"), 0600); got != 0640 {
		t.Errorf("group-writable dir: got %04o, want 0640", got)
	}
	// Private directory (0700): owner-only fallback, nothing is widened.
	// (testing.T.TempDir chmods its directory to 0777, so set 0700 explicitly.)
	private := t.TempDir()
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatalf("chmod private dir: %v", err)
	}
	if got := SharedFileMode(filepath.Join(private, "alert_history.jsonl"), 0600); got != 0600 {
		t.Errorf("private dir: got %04o, want 0600", got)
	}
	// Unstat-able path/parent and empty path: fallback unchanged.
	if got := SharedFileMode(filepath.Join(shared, "no", "such", "dir", "x.jsonl"), 0700); got != 0700 {
		t.Errorf("missing parent: got %04o, want 0700 (fallback)", got)
	}
	if got := SharedFileMode("", 0600); got != 0600 {
		t.Errorf("empty path: got %04o, want 0600", got)
	}
}
