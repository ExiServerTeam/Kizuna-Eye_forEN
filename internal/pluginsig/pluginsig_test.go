package pluginsig

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mustKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key size = %d", len(pub))
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("private key size = %d", len(priv))
	}
	return pub, priv
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", p, err)
	}
	return p
}

// A signature produced for one file must verify against the same bytes and
// fail against anything else. This is the property the agent relies on.
func TestSignVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pub, priv := mustKeyPair(t)
	so := writeFile(t, dir, "Foo.so", []byte("ELF-like bytes here"))

	if err := SignFile(so, SigPath(so), priv); err != nil {
		t.Fatalf("SignFile: %v", err)
	}
	if err := VerifyFile(so, SigPath(so), pub); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	pub, priv := mustKeyPair(t)
	so := writeFile(t, dir, "Foo.so", []byte("original"))
	if err := SignFile(so, SigPath(so), priv); err != nil {
		t.Fatalf("SignFile: %v", err)
	}

	// Same length, different content: a size-only check would pass.
	writeFile(t, dir, "Foo.so", []byte("Original"))
	if err := VerifyFile(so, SigPath(so), pub); err == nil {
		t.Fatal("tampered file must not verify")
	}
}

func TestVerifyDetectsWrongKey(t *testing.T) {
	dir := t.TempDir()
	_, priv := mustKeyPair(t)
	otherPub, _ := mustKeyPair(t)
	so := writeFile(t, dir, "Foo.so", []byte("payload"))
	if err := SignFile(so, SigPath(so), priv); err != nil {
		t.Fatalf("SignFile: %v", err)
	}
	if err := VerifyFile(so, SigPath(so), otherPub); err == nil {
		t.Fatal("a signature from another key must not verify")
	}
}

// A missing .sig must be reported as ErrNoSignature so callers can decide
// whether that is fatal (require_signature=true) or merely "unsigned".
func TestVerifyFileMissingSignature(t *testing.T) {
	dir := t.TempDir()
	pub, _ := mustKeyPair(t)
	so := writeFile(t, dir, "Foo.so", []byte("payload"))

	err := VerifyFile(so, SigPath(so), pub)
	if err == nil {
		t.Fatal("missing signature must be an error")
	}
	if !errors.Is(err, ErrNoSignature) {
		t.Fatalf("error should wrap ErrNoSignature, got %v", err)
	}
}

func TestSigPath(t *testing.T) {
	if got := SigPath("/opt/plugins/Foo.so"); got != "/opt/plugins/Foo.so.sig" {
		t.Fatalf("SigPath = %q", got)
	}
}

func pemPublicKey(t *testing.T, pub ed25519.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// Operators paste keys from different tools; all documented encodings must
// parse to the same key.
func TestParsePublicKeyFormats(t *testing.T) {
	pub, _ := mustKeyPair(t)
	cases := map[string][]byte{
		"base64":       []byte(EncodePublicKey(pub) + "\n"),
		"hex":          []byte(hex.EncodeToString(pub)),
		"pem":          pemPublicKey(t, pub),
		"raw-base64":   []byte(base64.RawStdEncoding.EncodeToString(pub)),
		"with-comment": []byte("# operator key\n" + EncodePublicKey(pub) + "\n"),
		"indented":     []byte("  " + EncodePublicKey(pub) + "  "),
	}
	for name, data := range cases {
		got, err := ParsePublicKey(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !got.Equal(pub) {
			t.Fatalf("%s: parsed key differs", name)
		}
	}
}

func TestParsePublicKeyRejectsGarbage(t *testing.T) {
	if _, err := ParsePublicKey(nil); err == nil {
		t.Fatal("empty input must fail")
	}
	if _, err := ParsePublicKey([]byte("not a key")); err == nil {
		t.Fatal("garbage must fail")
	}
	// A private key PEM must not be silently accepted as a public key.
	_, priv := mustKeyPair(t)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := ParsePublicKey(pemBytes); err == nil {
		t.Fatal("private key PEM must not parse as a public key")
	}
}

func TestParsePrivateKeySeedAndFullKey(t *testing.T) {
	pub, priv := mustKeyPair(t)

	fromFull, err := ParsePrivateKey([]byte(EncodePrivateKey(priv)))
	if err != nil {
		t.Fatalf("full key: %v", err)
	}
	if !fromFull.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatal("full key round trip mismatch")
	}

	seed := priv.Seed()
	fromSeed, err := ParsePrivateKey([]byte(hex.EncodeToString(seed)))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !fromSeed.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatal("seed round trip mismatch")
	}
}

func TestParseSignatureFormats(t *testing.T) {
	dir := t.TempDir()
	_, priv := mustKeyPair(t)
	so := writeFile(t, dir, "Foo.so", []byte("payload"))
	if err := SignFile(so, SigPath(so), priv); err != nil {
		t.Fatalf("SignFile: %v", err)
	}
	sigFile, err := os.ReadFile(SigPath(so))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	trimmed := strings.TrimSpace(string(sigFile))

	for name, data := range map[string][]byte{
		"base64":  []byte(trimmed + "\n"),
		"comment": []byte("# sig\n" + trimmed + "\n"),
	} {
		got, err := ParseSignature(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != ed25519.SignatureSize {
			t.Fatalf("%s: length %d", name, len(got))
		}
	}

	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := ParseSignature([]byte(hex.EncodeToString(raw))); err != nil {
		t.Fatalf("hex signature: %v", err)
	}
}

// A truncated or oversized signature must be rejected, not panicked on.
func TestVerifyBytesRejectsBadSignature(t *testing.T) {
	pub, _ := mustKeyPair(t)
	if err := VerifyBytes([]byte("data"), []byte("AAAA"), pub, "x"); err == nil {
		t.Fatal("short signature must fail")
	}
	if err := VerifyBytes([]byte("data"), nil, pub, "x"); err == nil {
		t.Fatal("empty signature must fail")
	}
	// Wrong-length key must fail rather than panic inside ed25519.
	if err := VerifyBytes([]byte("data"), []byte("AAAA"), ed25519.PublicKey{1, 2}, "x"); err == nil {
		t.Fatal("bad key length must fail")
	}
}

// SignFile must produce a readable, single-line, owner-only file.
func TestSignFileOutputShape(t *testing.T) {
	dir := t.TempDir()
	_, priv := mustKeyPair(t)
	so := writeFile(t, dir, "Foo.so", []byte("payload"))
	if err := SignFile(so, SigPath(so), priv); err != nil {
		t.Fatalf("SignFile: %v", err)
	}
	info, err := os.Stat(SigPath(so))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Windows does not model POSIX permission bits (Go maps the mode onto the
	// read-only attribute only), so 0600 cannot be asserted there. The
	// deployment target is Linux, where the check below is what matters.
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
	data, err := os.ReadFile(SigPath(so))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Count(string(data), "\n") != 1 {
		t.Fatalf("expected a single trailing newline, got %q", string(data))
	}
}

// A random 64-byte blob must not be accepted as a signature for some other
// message (sanity check that we really verify rather than just parse).
func TestVerifyBytesRejectsRandomSignature(t *testing.T) {
	pub, _ := mustKeyPair(t)
	junk := make([]byte, ed25519.SignatureSize)
	if _, err := rand.Read(junk); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sig := []byte(base64.StdEncoding.EncodeToString(junk))
	if err := VerifyBytes([]byte("payload"), sig, pub, "junk"); err == nil {
		t.Fatal("random signature must not verify")
	}
}
