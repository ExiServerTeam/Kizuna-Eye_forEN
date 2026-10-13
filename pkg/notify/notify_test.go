package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"hello", 1, "h"},
		{"hello", 0, ""},
		{"こんにちは", 5, "こんにちは"},
		{"こんにちは", 3, "こん…"},
		{"こんにちは", 1, "こ"},
	}
	for _, c := range cases {
		got := truncateRunes(c.in, c.n)
		if got != c.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncateRunes(%q, %d) produced invalid UTF-8: %q", c.in, c.n, got)
		}
	}
}

func TestTruncateRunesLongASCII(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := truncateRunes(long, 4096)
	if utf8.RuneCountInString(got) != 4096 {
		t.Errorf("want 4096 runes, got %d", utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated string should end with ellipsis: %q", got[len(got)-5:])
	}
}

// sanitizeHeader must strip CR/LF so a value cannot inject email headers.
func TestSanitizeHeader(t *testing.T) {
	in := "ok\r\nBcc: attacker@example.com"
	got := sanitizeHeader(in)
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("sanitizeHeader left CR/LF: %q", got)
	}
	if got != "okBcc: attacker@example.com" {
		t.Errorf("unexpected result: %q", got)
	}
}
