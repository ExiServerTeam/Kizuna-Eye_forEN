// Package pluginsig implements detached Ed25519 signature verification for
// plugin (.so) files. This is the root fix for hardening item A-3 (G-3):
// loading a Go plugin executes its init()/package initializers, so a .so must
// be authenticated BEFORE plugin.Open is called.
//
// Design notes
//
//   - Signatures are detached: <plugin>.so is signed and the signature is
//     stored next to it as <plugin>.so.sig (see SigSuffix). The .so itself is
//     never modified, so its hash/size in the dashboard UI keeps meaning the
//     same thing.
//   - The signature covers the raw file bytes. No canonicalisation is applied:
//     the file is read once by the signer and once by the verifier.
//   - Key material is deliberately transport-agnostic so an operator can paste
//     a key produced by any tool:
//     PUBLIC KEY : PKIX PEM, hex, or base64 (standard or raw/unpadded).
//     PRIVATE KEY: PKCS#8 PEM, hex, or base64 of a 64-byte Ed25519 key or a
//     32-byte seed.
//     SIGNATURE  : base64 (standard or raw) or hex; comment lines starting
//     with '#' and all whitespace are ignored.
//   - Verification is fail-closed at the call sites: a missing public key, a
//     missing signature, or a mismatch is an error, never a warning.
package pluginsig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// SigSuffix is appended to a plugin path to locate its detached
	// signature (plugins/Foo.so -> plugins/Foo.so.sig).
	SigSuffix = ".sig"

	// Size caps. Keys and signatures are tiny; the cap keeps a hostile or
	// corrupt file from being read into memory before the check starts.
	sigMaxSize  = 8 << 10 // 8 KiB
	keyMaxSize  = 8 << 10 // 8 KiB
	ed25519Pub  = ed25519.PublicKeySize
	ed25519Priv = ed25519.PrivateKeySize
	ed25519Seed = ed25519.SeedSize
	sigLen      = ed25519.SignatureSize
)

// ErrNoSignature reports that a detached signature file does not exist.
// Callers that require a signature treat it as a hard failure.
var ErrNoSignature = errors.New("署名ファイルが見つかりません")

// SigPath returns the detached-signature path for a plugin path.
func SigPath(soPath string) string { return soPath + SigSuffix }

// GenerateKeyPair returns a fresh Ed25519 key pair.
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("鍵生成に失敗: %w", err)
	}
	return pub, priv, nil
}

// ============================================================
// Encoding helpers
// ============================================================

// EncodePublicKey returns the base64 (standard, padded) form of pub.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// EncodePrivateKey returns the base64 (standard, padded) form of priv.
func EncodePrivateKey(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv)
}

// ============================================================
// Parsing
// ============================================================

// stripKeyText removes PEM headers/footers, '#' comment lines and all
// whitespace so the remaining text can be hex/base64 decoded.
func stripKeyText(data []byte) []byte {
	var b bytes.Buffer
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b.WriteString(line)
	}
	return b.Bytes()
}

// decodeBase64 tries the usual base64 alphabets.
func decodeBase64(s string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}

// ParsePublicKey parses an Ed25519 public key from PEM, hex or base64 text.
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	if len(data) == 0 {
		return nil, errors.New("公開鍵が空です")
	}
	if len(data) > keyMaxSize {
		return nil, fmt.Errorf("公開鍵ファイルが大きすぎます (%d bytes)", len(data))
	}

	if block, _ := pem.Decode(data); block != nil {
		// Accept "PUBLIC KEY" (PKIX). Anything else (e.g. a private key
		// pasted by mistake) is rejected with a clear message so the
		// operator is not left with a silently-unusable key.
		if block.Type == "PUBLIC KEY" {
			k, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("PKIX 公開鍵のパースに失敗: %w", err)
			}
			pub, ok := k.(ed25519.PublicKey)
			if !ok {
				return nil, errors.New("公開鍵が Ed25519 ではありません")
			}
			return pub, nil
		}
		return nil, fmt.Errorf("未対応の PEM 形式です: %s (PUBLIC KEY を使用してください)", block.Type)
	}

	text := stripKeyText(data)

	if b, err := hex.DecodeString(string(text)); err == nil && len(b) == ed25519Pub {
		return ed25519.PublicKey(b), nil
	}
	if b, err := decodeBase64(string(text)); err == nil && len(b) == ed25519Pub {
		return ed25519.PublicKey(b), nil
	}
	return nil, fmt.Errorf("公開鍵を解釈できません（Ed25519 の %d バイト鍵を PEM / hex / base64 で指定してください）", ed25519Pub)
}

// ParsePrivateKey parses an Ed25519 private key from PKCS#8 PEM, hex or base64.
// A 32-byte value is treated as a seed; a 64-byte value as a full key.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	if len(data) == 0 {
		return nil, errors.New("秘密鍵が空です")
	}
	if len(data) > keyMaxSize {
		return nil, fmt.Errorf("秘密鍵ファイルが大きすぎます (%d bytes)", len(data))
	}

	if block, _ := pem.Decode(data); block != nil {
		if block.Type != "PRIVATE KEY" {
			return nil, fmt.Errorf("未対応の PEM 形式です: %s (PRIVATE KEY を使用してください)", block.Type)
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("PKCS#8 秘密鍵のパースに失敗: %w", err)
		}
		priv, ok := k.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("秘密鍵が Ed25519 ではありません")
		}
		return priv, nil
	}

	text := stripKeyText(data)

	if b, err := hex.DecodeString(string(text)); err == nil {
		return keyFromBytes(b)
	}
	if b, err := decodeBase64(string(text)); err == nil {
		return keyFromBytes(b)
	}
	return nil, errors.New("秘密鍵を解釈できません（PKCS#8 PEM / hex / base64 で指定してください）")
}

func keyFromBytes(b []byte) (ed25519.PrivateKey, error) {
	switch len(b) {
	case ed25519Seed:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519Priv:
		return ed25519.PrivateKey(b), nil
	default:
		return nil, fmt.Errorf("秘密鍵の長さが不正です: %d バイト（%d または %d を期待）", len(b), ed25519Seed, ed25519Priv)
	}
}

// ParseSignature parses a detached signature from base64 or hex text.
func ParseSignature(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("署名が空です")
	}
	if len(data) > sigMaxSize {
		return nil, fmt.Errorf("署名ファイルが大きすぎます (%d bytes)", len(data))
	}
	text := stripKeyText(data)
	if b, err := hex.DecodeString(string(text)); err == nil && len(b) == sigLen {
		return b, nil
	}
	if b, err := decodeBase64(string(text)); err == nil && len(b) == sigLen {
		return b, nil
	}
	return nil, fmt.Errorf("署名を解釈できません（Ed25519 の %d バイト署名を base64 / hex で指定してください）", sigLen)
}

// ============================================================
// File-oriented helpers
// ============================================================

// LoadPublicKey reads and parses a public key file.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("公開鍵ファイルを開けません (%s): %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("公開鍵のパスがディレクトリです: %s", path)
	}
	if info.Size() > keyMaxSize {
		return nil, fmt.Errorf("公開鍵ファイルが大きすぎます (%s: %d bytes)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("公開鍵ファイルの読み込みに失敗 (%s): %w", path, err)
	}
	pub, err := ParsePublicKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return pub, nil
}

// LoadPrivateKey reads and parses a private key file.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("秘密鍵ファイルを開けません (%s): %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("秘密鍵のパスがディレクトリです: %s", path)
	}
	if info.Size() > keyMaxSize {
		return nil, fmt.Errorf("秘密鍵ファイルが大きすぎます (%s: %d bytes)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("秘密鍵ファイルの読み込みに失敗 (%s): %w", path, err)
	}
	priv, err := ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return priv, nil
}

// ============================================================
// Sign / Verify
// ============================================================

// SignFile writes a detached signature of the file at soPath to sigPath.
// The signature file is created 0600 because only the verifier needs it.
func SignFile(soPath, sigPath string, priv ed25519.PrivateKey) error {
	if len(priv) != ed25519Priv {
		return errors.New("署名鍵が不正です")
	}
	data, err := os.ReadFile(soPath)
	if err != nil {
		return fmt.Errorf("署名対象の読み込みに失敗 (%s): %w", soPath, err)
	}
	sig := ed25519.Sign(priv, data)
	if err := os.WriteFile(sigPath, []byte(EncodeSignature(sig)+"\n"), 0o600); err != nil {
		return fmt.Errorf("署名ファイルの書き込みに失敗 (%s): %w", sigPath, err)
	}
	return nil
}

// VerifyFile checks the detached signature at sigPath against the file at
// soPath. A missing signature file returns an error wrapping ErrNoSignature.
func VerifyFile(soPath, sigPath string, pub ed25519.PublicKey) error {
	if len(pub) != ed25519Pub {
		return errors.New("検証鍵が不正です")
	}
	if _, err := os.Stat(sigPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNoSignature, sigPath)
		}
		return fmt.Errorf("署名ファイルを開けません (%s): %w", sigPath, err)
	}
	sigBytes, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("署名ファイルの読み込みに失敗 (%s): %w", sigPath, err)
	}
	data, err := os.ReadFile(soPath)
	if err != nil {
		return fmt.Errorf("署名対象の読み込みに失敗 (%s): %w", soPath, err)
	}
	return VerifyBytes(data, sigBytes, pub, soPath)
}

// VerifyBytes checks a detached signature held in memory against data. It is
// used by the upload path, where the signature arrives as a multipart part and
// no .sig file exists yet.
func VerifyBytes(data, sigBytes []byte, pub ed25519.PublicKey, label string) error {
	if len(pub) != ed25519Pub {
		return errors.New("検証鍵が不正です")
	}
	sig, err := ParseSignature(sigBytes)
	if err != nil {
		return fmt.Errorf("署名の解釈に失敗 (%s): %w", label, err)
	}
	if !ed25519.Verify(pub, data, sig) {
		return fmt.Errorf("署名が一致しません (%s)", label)
	}
	return nil
}

// EncodeSignature returns the base64 form written into a .sig file.
func EncodeSignature(sig []byte) string {
	return base64.StdEncoding.EncodeToString(sig)
}
