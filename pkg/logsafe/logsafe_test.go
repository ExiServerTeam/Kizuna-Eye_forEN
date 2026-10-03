package logsafe

import (
	"strings"
	"testing"
)

// TestFieldNeutralisesForging pins the log-forging defence: a value containing
// a newline / control character and a fake level tag must not produce a line
// break or an unescaped "[INFO]"/"[WARN]" marker in the log.
func TestFieldNeutralisesForging(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"newline+tag", "a\n[INFO] FORGED"},
		{"cr", "a\r[WARN] x"},
		{"tab", "a\t[ERROR] y"},
		{"nul", "a\x00b"},
		{"esc", "a\x1b[31mred"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Field(tc.in)

			// 1) No raw CR/LF/tab/NUL/ESC may survive.
			for _, bad := range []string{"\n", "\r", "\t", "\x00", "\x1b"} {
				if strings.Contains(got, bad) {
					t.Errorf("Field(%q) = %q still contains control char %q", tc.in, got, bad)
				}
			}

			// 2) Every surviving '[' must be escaped as '\\['.
			for i := 0; i < len(got); i++ {
				if got[i] == '[' && (i == 0 || got[i-1] != '\\') {
					t.Errorf("Field(%q) = %q has an unescaped '['", tc.in, got)
				}
			}
		})
	}
}

func TestFieldKeepsNormalText(t *testing.T) {
	if got := Field("shige"); got != "shige" {
		t.Errorf("got %q, want shige", got)
	}
	if got := Field(""); got != "" {
		t.Errorf("empty input should stay empty, got %q", got)
	}
}
