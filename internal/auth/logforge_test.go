package auth

import (
	"strings"
	"testing"

	"Kizuna-Eye/pkg/logsafe"
)

// TestSharedLogsafeFieldNeutralisesForging pins the log-forging defence: a
// username containing a newline / control character and a fake level tag must
// not produce a line break or an unescaped "[INFO]"/"[WARN]" marker in the
// log. The helper moved to pkg/logsafe (L-11); this keeps the auth-side
// regression test next to its call site.
func TestSharedLogsafeFieldNeutralisesForging(t *testing.T) {
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
			got := logsafe.Field(tc.in)

			// 1) No raw CR/LF/tab/NUL/ESC may survive.
			for _, bad := range []string{"\n", "\r", "\t", "\x00", "\x1b"} {
				if strings.Contains(got, bad) {
					t.Errorf("sanitizeLogField(%q) = %q still contains control char %q", tc.in, got, bad)
				}
			}

			// 2) Every surviving '[' must be escaped as '\\['.
			for i := 0; i < len(got); i++ {
				if got[i] == '[' && (i == 0 || got[i-1] != '\\') {
					t.Errorf("sanitizeLogField(%q) = %q has an unescaped '['", tc.in, got)
				}
			}
		})
	}
}

func TestSharedLogsafeFieldKeepsNormalText(t *testing.T) {
	if got := logsafe.Field("shige"); got != "shige" {
		t.Errorf("got %q, want shige", got)
	}
	if got := logsafe.Field(""); got != "" {
		t.Errorf("empty input should stay empty, got %q", got)
	}
}
