package notify

import (
	"strings"
	"testing"
)

func TestEscapeSlackMrkdwn(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"<!channel>", "&lt;!channel&gt;"},
		{"<@U123>", "&lt;@U123&gt;"},
		{"<http://evil|click>", "&lt;http://evil|click&gt;"},
		{"a & b", "a &amp; b"},
		{"plain text", "plain text"},
		{"<script>", "&lt;script&gt;"},
	}
	for _, c := range cases {
		got := escapeSlackMrkdwn(c.in)
		if got != c.want {
			t.Errorf("escapeSlackMrkdwn(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEscapeSlackMrkdwnNoRawAngleBrackets(t *testing.T) {
	// An attacker-controlled username with a mention must not survive as a
	// live mention after escaping.
	dangerous := "nonexistent_<!channel>_probe"
	got := escapeSlackMrkdwn(dangerous)
	if strings.Contains(got, "<!") || strings.Contains(got, "<@") {
		t.Errorf("escaping left a live mention marker: %q", got)
	}
}
