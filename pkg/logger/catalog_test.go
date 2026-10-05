package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoggerInfoTEmitsJSONWithBothLanguages verifies that InfoT writes one
// JSON Lines entry carrying both message (ja) and message_en.
func TestLoggerInfoTEmitsJSONWithBothLanguages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.log")
	lg := NewLogger(&Options{LogFile: path, Level: DEBUG, Prefix: "[AGENT]"})
	lg.InfoT("agent.start", 2.5, "ws://x")
	lg.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "{") {
		t.Fatalf("expected JSON Lines, got: %s", line)
	}
	var entry map[string]interface{}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, line)
	}
	if got, _ := entry["message"].(string); !strings.Contains(got, "エージェント起動") {
		t.Errorf("message = %q, want Japanese rendering", got)
	}
	if got, _ := entry["message_en"].(string); !strings.Contains(got, "Agent started") {
		t.Errorf("message_en = %q, want English rendering", got)
	}
	if got, _ := entry["level"].(string); got != "INFO" {
		t.Errorf("level = %q, want INFO", got)
	}
	if got, _ := entry["prefix"].(string); got != "AGENT" {
		t.Errorf("prefix = %q, want AGENT (brackets trimmed)", got)
	}
}

// TestMsgFallsBackToJapaneseForUnknownKey ensures a missing key stays visible.
func TestMsgUnknownKey(t *testing.T) {
	if got := msg("en", "no.such.key"); got != "no.such.key" {
		t.Errorf("msg(unknown) = %q, want the key itself", got)
	}
}
