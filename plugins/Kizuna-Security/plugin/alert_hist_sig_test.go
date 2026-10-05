package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// sigOf computes the HMAC the dashboard would store for a history map with
// the "sig" field removed.
func sigOf(t *testing.T, key []byte, m map[string]interface{}) string {
	t.Helper()
	delete(m, "sig")
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	h := hmac.New(sha256.New, key)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func writeLine(t *testing.T, f *os.File, m map[string]interface{}) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAlertHistorySigsOK(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	dir := t.TempDir()
	path := filepath.Join(dir, "ah.jsonl")
	f, _ := os.Create(path)
	m := map[string]interface{}{"type": "t", "title": "a"}
	m["sig"] = sigOf(t, key, map[string]interface{}{"type": "t", "title": "a"})
	writeLine(t, f, m)
	f.Close()

	reason, line := verifyAlertHistorySigs(path, key)
	if reason != "" || line != 0 {
		t.Fatalf("valid sig flagged: reason=%q line=%d", reason, line)
	}
}

func TestVerifyAlertHistorySigsSkipsUnsigned(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	dir := t.TempDir()
	path := filepath.Join(dir, "ah.jsonl")
	f, _ := os.Create(path)
	writeLine(t, f, map[string]interface{}{"type": "t", "title": "a"}) // no sig
	f.Close()

	reason, _ := verifyAlertHistorySigs(path, key)
	if reason != "" {
		t.Fatalf("unsigned line must be skipped, got reason=%q", reason)
	}
}

func TestVerifyAlertHistorySigsDetectsTamper(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	dir := t.TempDir()
	path := filepath.Join(dir, "ah.jsonl")
	f, _ := os.Create(path)
	// Sign the original, then change the title.
	orig := map[string]interface{}{"type": "t", "title": "orig"}
	sig := sigOf(t, key, map[string]interface{}{"type": "t", "title": "orig"})
	tampered := map[string]interface{}{"type": "t", "title": "tampered", "sig": sig}
	writeLine(t, f, tampered)
	f.Close()
	_ = orig

	reason, line := verifyAlertHistorySigs(path, key)
	if reason == "" || line != 1 {
		t.Fatalf("tampered line not detected: reason=%q line=%d", reason, line)
	}
}
