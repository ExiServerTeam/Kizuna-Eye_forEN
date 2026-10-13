package report

import (
	"strings"
	"testing"
	"time"

	"Kizuna-Eye/pkg/alert"
)

func TestGenerateEmpty(t *testing.T) {
	now := time.Now()
	res := Generate(nil, Options{Now: now, Period: 7 * 24 * time.Hour})
	if res.Total != 0 {
		t.Errorf("Total = %d, want 0", res.Total)
	}
	if !strings.Contains(res.Markdown, "アラートはありませんでした") {
		t.Errorf("missing empty summary:\n%s", res.Markdown)
	}
	if res.Subject == "" {
		t.Error("empty subject")
	}
}

func TestGenerateCountsAndHighlights(t *testing.T) {
	now := time.Now()
	entries := []alert.HistoryEntry{
		{Type: "security_ssh_failed", Level: "warning", Title: "SSH失敗", Message: "u from 1.2.3.4", Timestamp: now.Add(-time.Hour)},
		{Type: "security_integrity", Level: "critical", Title: "改ざん", Message: "/etc/passwd", Timestamp: now.Add(-2 * time.Hour)},
		{Type: "security_listen_port", Level: "warning", Title: "新規ポート", Message: "44444", Timestamp: now.Add(-3 * time.Hour)},
		// Outside the window; must be ignored.
		{Type: "security_ssh_failed", Level: "warning", Title: "old", Timestamp: now.Add(-30 * 24 * time.Hour)},
	}
	res := Generate(entries, Options{Now: now, Period: 7 * 24 * time.Hour})
	if res.Total != 3 {
		t.Errorf("Total = %d, want 3", res.Total)
	}
	if res.Critical != 1 {
		t.Errorf("Critical = %d, want 1", res.Critical)
	}
	if !strings.Contains(res.Markdown, "改ざん") {
		t.Errorf("missing critical highlight:\n%s", res.Markdown)
	}
	// Recommendations should mention SSH and integrity.
	if !strings.Contains(res.Markdown, "改善提案") {
		t.Errorf("missing recommendations section")
	}
}

func TestGenerateSanitizesNewlines(t *testing.T) {
	now := time.Now()
	entries := []alert.HistoryEntry{
		{Type: "security_integrity", Level: "critical", Title: "line1\nline2", Message: "a\r\nb", Timestamp: now},
	}
	res := Generate(entries, Options{Now: now})
	if strings.Contains(res.Markdown, "line1\nline2") {
		t.Errorf("newline not sanitized in title:\n%s", res.Markdown)
	}
}
