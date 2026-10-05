package alert

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"Kizuna-Eye/pkg/notify"
)

// TestSignVerifyHistoryLineRoundTrip verifies a signed line round-trips.
func TestSignVerifyHistoryLineRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	e := HistoryEntry{Type: "test", Level: "warning", Title: "t", Message: "m", Timestamp: time.Now().UTC()}
	sig, err := signHistoryEntry(key, e)
	if err != nil || sig == "" {
		t.Fatalf("sign failed: %v (sig=%q)", err, sig)
	}
	e.Sig = sig
	b, _ := json.Marshal(e)
	ok, err := VerifyHistoryLineSig(key, b)
	if err != nil || !ok {
		t.Fatalf("VerifyHistoryLineSig = (%v,%v), want (true,nil)", ok, err)
	}
}

// TestVerifyHistoryLineSkipsUnsigned verifies a legacy (unsigned) line is not
// flagged, so enabling signing never produces a false positive.
func TestVerifyHistoryLineSkipsUnsigned(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	e := HistoryEntry{Type: "test", Level: "info", Title: "t", Message: "m", Timestamp: time.Now().UTC()}
	b, _ := json.Marshal(e) // no Sig
	ok, err := VerifyHistoryLineSig(key, b)
	if err != nil || !ok {
		t.Fatalf("unsigned line should be skipped: (%v,%v)", ok, err)
	}
}

// TestVerifyHistoryLineDetectsTamper verifies a modified line is detected.
func TestVerifyHistoryLineDetectsTamper(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	e := HistoryEntry{Type: "test", Level: "critical", Title: "orig", Message: "m", Timestamp: time.Now().UTC()}
	sig, _ := signHistoryEntry(key, e)
	e.Sig = sig
	e.Title = "tampered" // change after signing
	b, _ := json.Marshal(e)
	ok, err := VerifyHistoryLineSig(key, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("tampered line must fail verification")
	}
}

// TestVerifyWithWrongKeyFails verifies a signature made with another key fails.
func TestVerifyWithWrongKeyFails(t *testing.T) {
	k1 := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	k2 := []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	e := HistoryEntry{Type: "test", Level: "info", Title: "t", Timestamp: time.Now().UTC()}
	sig, _ := signHistoryEntry(k1, e)
	e.Sig = sig
	b, _ := json.Marshal(e)
	ok, _ := VerifyHistoryLineSig(k2, b)
	if ok {
		t.Fatal("signature from another key must not verify")
	}
}

// TestLoadOrCreateAlertHistoryKey verifies creation, reuse, and empty-path.
func TestLoadOrCreateAlertHistoryKey(t *testing.T) {
	if k, err := LoadOrCreateAlertHistoryKey(""); err != nil || k != nil {
		t.Fatalf("empty path should return (nil,nil), got (%v,%v)", k, err)
	}
	// Use a not-yet-existing subdirectory: os.MkdirAll is a no-op for an
	// existing dir (t.TempDir() itself would keep the framework's mode), so
	// the 0750 assertion only makes sense for a directory this call creates.
	dir := filepath.Join(t.TempDir(), "keys")
	path := filepath.Join(dir, "alert_history.key")
	k1, err := LoadOrCreateAlertHistoryKey(path)
	if err != nil || len(k1) == 0 {
		t.Fatalf("create failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not honor Unix permission bits, so only assert the mode
	// on platforms where it is meaningful.
	// H-1: the key is created 0640 (group read) so the agent user can verify
	// signatures; the dir is 0750 (group traverse).
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0640 {
			t.Errorf("key mode = %v, want 0640", info.Mode().Perm())
		}
		di, derr := os.Stat(dir)
		if derr != nil {
			t.Fatal(derr)
		}
		if di.Mode().Perm() != 0750 {
			t.Errorf("dir mode = %v, want 0750", di.Mode().Perm())
		}
	}
	k2, err := LoadOrCreateAlertHistoryKey(path)
	if err != nil || string(k1) != string(k2) {
		t.Fatalf("existing key should be reused: %v", err)
	}
}

// TestHistoryPersistenceSignsLines verifies that with a key set, persisted
// lines carry a verifiable sig, and without a key they do not.
func TestHistoryPersistenceSignsLines(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	dir := t.TempDir()
	path := filepath.Join(dir, "ah.jsonl")
	h := NewHistory(10)
	h.SetSigningKey(key)
	h.SetPersistence(path)
	h.Add(&notify.Alert{Type: "t", Level: notify.LevelWarning, Title: "a", Message: "b", Timestamp: time.Now().UTC()})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	if !strings.Contains(line, "\"sig\":") {
		t.Fatalf("persisted line should carry sig: %s", line)
	}
	ok, err := VerifyHistoryLineSig(key, []byte(line))
	if err != nil || !ok {
		t.Fatalf("persisted sig should verify: (%v,%v)", ok, err)
	}

	// No key -> no sig (backward compatible).
	path2 := filepath.Join(dir, "ah2.jsonl")
	h2 := NewHistory(10)
	h2.SetPersistence(path2)
	h2.Add(&notify.Alert{Type: "t", Level: notify.LevelInfo, Title: "a", Timestamp: time.Now().UTC()})
	data2, _ := os.ReadFile(path2)
	if strings.Contains(string(data2), "\"sig\":") {
		t.Fatalf("unsigned line should not contain sig: %s", data2)
	}
}
