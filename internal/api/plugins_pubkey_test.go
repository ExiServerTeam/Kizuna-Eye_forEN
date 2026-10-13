package api

import (
	"os"
	"path/filepath"
	"testing"

	"Kizuna-Eye/internal/pluginsig"
	"Kizuna-Eye/pkg/config"
)

// TestLoadPublicKeyNoKeyConfigured pins the gate for the origin fix: when no
// public_key_file is set, loadPublicKey returns (nil, nil) and the upload path
// must treat every plugin as unsigned (third_party). It must not error, or a
// default install could not upload a third-party plugin at all.
func TestLoadPublicKeyNoKeyConfigured(t *testing.T) {
	p := &PluginManager{}
	pub, err := p.loadPublicKey()
	if err != nil {
		t.Fatalf("no key configured must not error, got: %v", err)
	}
	if pub != nil {
		t.Fatalf("no key configured must return a nil key, got %v", pub)
	}
}

// TestLoadPublicKeyVerifiesConfiguredKey checks that a configured key is loaded
// and cached, so a signed upload is recognised as "official" even when
// require_signature is false (the origin-classification fix).
func TestLoadPublicKeyVerifiesConfiguredKey(t *testing.T) {
	dir := t.TempDir()
	pub, _, err := pluginsig.GenerateKeyPair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pubPath := filepath.Join(dir, "plugin_signing.pub")
	if err := os.WriteFile(pubPath, []byte(pluginsig.EncodePublicKey(pub)+"\n"), 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}

	p := &PluginManager{
		policy: config.PluginSecurityConfig{
			// require_signature deliberately false: verification must still run
			// whenever a key is configured.
			RequireSignature: false,
			PublicKeyFile:    pubPath,
		},
	}

	got, err := p.loadPublicKey()
	if err != nil {
		t.Fatalf("loadPublicKey: %v", err)
	}
	if got == nil {
		t.Fatal("a configured key must be loaded even when require_signature is false")
	}
	if !got.Equal(pub) {
		t.Fatal("loaded key does not match the configured one")
	}

	// Cached: a second call returns the same value without re-reading.
	again, err := p.loadPublicKey()
	if err != nil || again == nil || !again.Equal(pub) {
		t.Fatalf("cached load mismatch: key=%v err=%v", again, err)
	}
}

// TestLoadPublicKeyMissingFileIsCachedError checks that a configured-but-missing
// key is reported (not silently ignored) so the upload is refused instead of
// downgrading every plugin to third_party.
func TestLoadPublicKeyMissingFileIsCachedError(t *testing.T) {
	p := &PluginManager{
		policy: config.PluginSecurityConfig{
			RequireSignature: true,
			PublicKeyFile:    filepath.Join(t.TempDir(), "does-not-exist.pub"),
		},
	}
	if _, err := p.loadPublicKey(); err == nil {
		t.Fatal("a missing key file must be an error")
	}
}
