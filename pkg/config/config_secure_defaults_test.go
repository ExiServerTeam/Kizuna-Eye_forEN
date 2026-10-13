package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTemp writes content to a temp file and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "dashboard_config.json")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestNoFileGetsSecureDefaults: a brand-new install (no config) must have
// public_viewer disabled (the read-only viewer endpoints stay behind login).
func TestNoFileGetsSecureDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	cfg, err := LoadDashboardConfig(missing)
	if err != nil {
		t.Fatalf("LoadDashboardConfig: %v", err)
	}
	if cfg.Auth.IsPublicViewer() {
		t.Fatal("new install (no file) must default public_viewer=false")
	}
	if cfg.ConfigVersion != CurrentConfigVersion {
		t.Fatalf("new install must be stamped with config_version=%d, got %d", CurrentConfigVersion, cfg.ConfigVersion)
	}
}

// TestLegacyConfigKeepsPublicViewer: a config without config_version (an
// existing installation) must keep the historical public_viewer=true default
// when the key is absent, so an upgrade never changes its behavior.
func TestLegacyConfigKeepsPublicViewer(t *testing.T) {
	p := writeTemp(t, `{"listen_addr":":8080","auth":{"enabled":true}}`)
	cfg, err := LoadDashboardConfig(p)
	if err != nil {
		t.Fatalf("LoadDashboardConfig: %v", err)
	}
	if !cfg.Auth.IsPublicViewer() {
		t.Fatal("legacy config (no config_version) must keep public_viewer default=true")
	}
}

// TestVersionedConfigGetsSecureDefault: a config that declares the current
// config_version gets the secure default for an absent public_viewer.
func TestVersionedConfigGetsSecureDefault(t *testing.T) {
	p := writeTemp(t, `{"config_version":2,"listen_addr":":8080","auth":{"enabled":true}}`)
	cfg, err := LoadDashboardConfig(p)
	if err != nil {
		t.Fatalf("LoadDashboardConfig: %v", err)
	}
	if cfg.Auth.IsPublicViewer() {
		t.Fatal("versioned config with absent public_viewer must default to false")
	}
}

// TestExplicitPublicViewerHonored: an explicitly set public_viewer is honored
// regardless of config_version, so an operator can always opt in/out.
func TestExplicitPublicViewerHonored(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{"versioned true", `{"config_version":2,"auth":{"public_viewer":true}}`, true},
		{"versioned false", `{"config_version":2,"auth":{"public_viewer":false}}`, false},
		{"legacy true", `{"auth":{"public_viewer":true}}`, true},
		{"legacy false", `{"auth":{"public_viewer":false}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTemp(t, tc.content)
			cfg, err := LoadDashboardConfig(p)
			if err != nil {
				t.Fatalf("LoadDashboardConfig: %v", err)
			}
			if cfg.Auth.IsPublicViewer() != tc.want {
				t.Fatalf("public_viewer = %v, want %v", cfg.Auth.IsPublicViewer(), tc.want)
			}
		})
	}
}
