package logger

import "testing"

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"debug": DEBUG,
		"info":  INFO,
		"warn":  WARN,
		"error": ERROR,
		"fatal": FATAL,
		"":      INFO, // unknown/empty falls back to INFO
		"bogus": INFO,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
