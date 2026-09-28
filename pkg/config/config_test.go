package config

import (
	"encoding/json"
	"testing"
)

// Regression: plugins_upload_enabled defaults to false when unset.
func TestIsUploadEnabledDefaultsFalse(t *testing.T) {
	var cfg DashboardConfig
	if cfg.IsUploadEnabled() {
		t.Fatal("unset plugins_upload_enabled must be false")
	}

	b := true
	cfg.PluginsUpload = &b
	if !cfg.IsUploadEnabled() {
		t.Fatal("plugins_upload_enabled=true must be true")
	}

	f := false
	cfg.PluginsUpload = &f
	if cfg.IsUploadEnabled() {
		t.Fatal("plugins_upload_enabled=false must be false")
	}
}

// log_level must be parsed from JSON into DashboardConfig.LogLevel.
func TestLogLevelParsed(t *testing.T) {
	var cfg DashboardConfig
	if err := json.Unmarshal([]byte(`{"log_level":"warn"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "warn")
	}
}

// ResolvePluginsDir must honor an explicit plugins_dir.
func TestResolvePluginsDirExplicit(t *testing.T) {
	cfg := &DashboardConfig{PluginsDir: "plugins"}
	if got := cfg.ResolvePluginsDir(); got == "" {
		t.Fatal("ResolvePluginsDir returned empty")
	}
}
