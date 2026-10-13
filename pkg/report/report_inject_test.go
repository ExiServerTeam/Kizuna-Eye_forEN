package report

import (
	"strings"
	"testing"
	"time"

	"Kizuna-Eye/pkg/alert"
)

// TestGenerateSanitizesTypeKey is a regression test for Markdown/structure
// injection via the alert Type. The Type is built from agent/plugin-reported
// data ("security_" + category), so an attacker who can emit a security event
// controls it. A newline in the Type used to be written verbatim into the
// "検知種別" bullet list, letting it inject an extra Markdown bullet or a
// heading. Title and Message were sanitized; the Type key was not.
func TestGenerateSanitizesTypeKey(t *testing.T) {
	now := time.Now()
	entries := []alert.HistoryEntry{
		{
			Type:      "security_evil\n## Injected Heading",
			Level:     "warning",
			Title:     "t",
			Message:   "m",
			Timestamp: now.Add(-time.Minute),
		},
	}
	res := Generate(entries, Options{Now: now, Period: time.Hour})

	if strings.Contains(res.Markdown, "\n## Injected Heading") {
		t.Errorf("Type newline was not sanitized; injected heading present:\n%s", res.Markdown)
	}
	// The sanitized form must still appear (collapsed to a single line).
	if !strings.Contains(res.Markdown, "security_evil ## Injected Heading") {
		t.Errorf("sanitized type not found in report:\n%s", res.Markdown)
	}
}

// TestGenerateSanitizesCarriageReturnInType covers \r as well.
func TestGenerateSanitizesCarriageReturnInType(t *testing.T) {
	now := time.Now()
	entries := []alert.HistoryEntry{
		{Type: "security_a\r\nb", Level: "warning", Title: "t", Message: "m", Timestamp: now},
	}
	res := Generate(entries, Options{Now: now, Period: time.Hour})
	if strings.ContainsAny(res.Markdown, "\r") {
		t.Errorf("carriage return survived sanitization:\n%q", res.Markdown)
	}
}
