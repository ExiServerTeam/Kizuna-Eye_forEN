package pluginsig

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedAppendAfterTrailer documents (and locks in) the behavior when
// bytes are appended AFTER a valid embedded trailer: the magic is no longer at
// the end, so it is no longer detected as an embedded signature, and with no
// detached .sig the file is refused. This must stay a refusal.
func TestEmbeddedAppendAfterTrailer(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signed, err := EmbedSignatureBytes([]byte("LEGIT-BODY"), priv)
	if err != nil {
		t.Fatal(err)
	}
	attacked := append(append([]byte{}, signed...), []byte("MALICIOUS")...)

	dir := t.TempDir()
	so := filepath.Join(dir, "x.so")
	if err := os.WriteFile(so, attacked, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFileAny(so, pub); err == nil {
		t.Fatal("appended file was accepted; signature can be bypassed")
	}
}

// TestEmbeddedBodySwapRejected proves that replacing the body while keeping a
// valid trailer is rejected (the signature does not match the new body).
func TestEmbeddedBodySwapRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	orig := []byte("LEGIT-BODY")
	signed, _ := EmbedSignatureBytes(orig, priv)

	swapped := append([]byte("EVIL-BODY!"), signed[len(orig):]...)
	ok, err := VerifyEmbedded(swapped, pub)
	if !ok {
		t.Fatal("trailer should have been detected")
	}
	if err == nil {
		t.Fatal("body swap accepted; embedded signature does not cover the body")
	}
}

// TestEmbeddedValidAccepted is the positive control.
func TestEmbeddedValidAccepted(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signed, _ := EmbedSignatureBytes([]byte("BODY"), priv)
	ok, err := VerifyEmbedded(signed, pub)
	if !ok || err != nil {
		t.Fatalf("valid embedded signature rejected: ok=%v err=%v", ok, err)
	}
}

// TestEmbeddedMagicInsideBodyIsNotMistaken guards the false-positive case: a
// body that merely CONTAINS the magic bytes (but not at the trailer position)
// must not be treated as an embedded signature, so it falls through to the
// detached check.
func TestEmbeddedMagicInsideBodyIsNotMistaken(t *testing.T) {
	body := append([]byte("ELF..."), []byte(EmbedMagic)...)
	body = append(body, bytes.Repeat([]byte{0}, ed25519.SignatureSize+4)...)
	if HasEmbeddedSignature(body) {
		t.Fatal("magic inside the body was mistaken for a trailer")
	}
}
