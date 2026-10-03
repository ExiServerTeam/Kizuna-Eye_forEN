package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAbsFromConfigDir(t *testing.T) {
	dir := filepath.FromSlash("/opt/kizuna-eye/data")
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"logs/alert_history.jsonl", filepath.Join(dir, "logs", "alert_history.jsonl")},
		{"alert_history.jsonl", filepath.Join(dir, "alert_history.jsonl")},
	}
	for _, tc := range cases {
		if got := absFromConfigDir(dir, tc.in); got != tc.want {
			t.Errorf("absFromConfigDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Without a config dir the value is kept verbatim.
	if got := absFromConfigDir("", "logs/x.log"); got != "logs/x.log" {
		t.Errorf("no config dir: got %q, want the value unchanged", got)
	}
}

func TestAbsFromConfigDirKeepsAbsolutePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX absolute paths are not absolute on Windows")
	}
	if got := absFromConfigDir("/opt/kizuna-eye/data", "/var/log/x.log"); got != "/var/log/x.log" {
		t.Errorf("absolute path was rewritten: %q", got)
	}
}

func TestResolvePathsAnchorsDataAtConfigDir(t *testing.T) {
	dir := t.TempDir()

	d := &DashboardConfig{
		LogFile:          "logs/dashboard.log",
		AlertHistoryFile: "logs/alert_history.jsonl",
		PluginsDir:       "plugins",
		StaticDir:        "./web/static",
	}
	d.resolvePaths(dir)
	if want := filepath.Join(dir, "logs", "dashboard.log"); d.LogFile != want {
		t.Errorf("LogFile = %q, want %q", d.LogFile, want)
	}
	if want := filepath.Join(dir, "logs", "alert_history.jsonl"); d.AlertHistoryFile != want {
		t.Errorf("AlertHistoryFile = %q, want %q", d.AlertHistoryFile, want)
	}
	if want := filepath.Join(dir, "plugins"); d.PluginsDir != want {
		t.Errorf("PluginsDir = %q, want %q", d.PluginsDir, want)
	}
	// The UI directory is shipped in the repository, not next to the config.
	if d.StaticDir != "./web/static" {
		t.Errorf("static_dir must not be anchored: %q", d.StaticDir)
	}

	a := &AgentConfig{LogFile: "logs/agent.log", PluginsDir: "plugins"}
	a.resolvePaths(dir)
	if want := filepath.Join(dir, "logs", "agent.log"); a.LogFile != want {
		t.Errorf("agent LogFile = %q, want %q", a.LogFile, want)
	}
	if want := filepath.Join(dir, "plugins"); a.PluginsDir != want {
		t.Errorf("agent PluginsDir = %q, want %q", a.PluginsDir, want)
	}

	// Empty values stay empty so the documented defaults keep applying
	// (users.json/sessions.json next to the config, plugins/ next to the binary).
	e := &AgentConfig{}
	e.resolvePaths(dir)
	if e.LogFile != "" || e.PluginsDir != "" {
		t.Errorf("empty values must stay empty: %+v", e)
	}
}

func TestLoadAgentConfigAnchorsLogFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent_config.json")
	body := `{"dashboard_url":"ws://localhost:8080/ws","interval":1,"disk_path":"/","log_file":"logs/agent.log"}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	if want := filepath.Join(dir, "logs", "agent.log"); cfg.LogFile != want {
		t.Errorf("LogFile = %q, want %q", cfg.LogFile, want)
	}
}

func TestLoadDashboardConfigAnchorsDataPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dashboard_config.json")
	body := `{"listen_addr":":8080","log_file":"logs/dashboard.log",` +
		`"alert_history_file":"logs/alert_history.jsonl","plugins_dir":"plugins"}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDashboardConfig(path)
	if err != nil {
		t.Fatalf("LoadDashboardConfig: %v", err)
	}
	if want := filepath.Join(dir, "logs", "dashboard.log"); cfg.LogFile != want {
		t.Errorf("LogFile = %q, want %q", cfg.LogFile, want)
	}
	if want := filepath.Join(dir, "logs", "alert_history.jsonl"); cfg.AlertHistoryFile != want {
		t.Errorf("AlertHistoryFile = %q, want %q", cfg.AlertHistoryFile, want)
	}
	if want := filepath.Join(dir, "plugins"); cfg.PluginsDir != want {
		t.Errorf("PluginsDir = %q, want %q", cfg.PluginsDir, want)
	}
}
