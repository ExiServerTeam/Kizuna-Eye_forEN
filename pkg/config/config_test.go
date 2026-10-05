package config

import (
	"encoding/json"
	"strings"
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

// Regression for the Discord 429 alert-loss bug: discord_max_retries is a *int
// so that an absent key keeps the documented default (5) in pkg/notify, while an
// explicit 0 still means "retry disabled".
//
// The same test pins the write-back behaviour: AlertConfigHandler.persistConfig
// marshals the whole struct, and omitempty must keep a nil pointer out of the
// file (otherwise the config gains a meaningless "discord_max_retries": null).
func TestDiscordMaxRetriesPointerSemantics(t *testing.T) {
	var cfg DashboardConfig
	if cfg.Notifications.DiscordMaxRetries != nil {
		t.Fatal("an absent discord_max_retries must stay nil")
	}
	out, err := json.Marshal(&cfg.Notifications)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "discord_max_retries") {
		t.Errorf("nil pointer must be omitted when writing the config: %s", out)
	}

	// An explicit 0 must survive the round trip: it is the documented way to
	// disable retry (legacy behaviour).
	zero := 0
	cfg.Notifications.DiscordMaxRetries = &zero
	out, err = json.Marshal(&cfg.Notifications)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"discord_max_retries":0`) {
		t.Errorf("explicit 0 must be serialized: %s", out)
	}
	var back NotificationsConfig
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.DiscordMaxRetries == nil || *back.DiscordMaxRetries != 0 {
		t.Errorf("round trip lost the explicit 0: %v", back.DiscordMaxRetries)
	}
}
