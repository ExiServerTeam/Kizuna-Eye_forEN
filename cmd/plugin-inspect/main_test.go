package main

import (
	"reflect"
	"testing"
	"time"
)

func TestDeriveNameFromPath(t *testing.T) {
	cases := map[string]string{
		"/opt/kizuna-eye/bin/plugins/kizuna_security.so": "kizuna_security",
		"C:\\\\plugins\\\\my_plugin.so":                  "my_plugin",
		"plain.so":                                       "plain",
		"/a/b/c.so.so":                                   "c.so",
		"/nope/no_ext":                                   "no_ext",
	}
	for in, want := range cases {
		if got := deriveNameFromPath(in); got != want {
			t.Errorf("deriveNameFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// A method with parameters must NOT be called (would panic) and must return
// the zero value instead.
type hostilePlugin struct{}

func (hostilePlugin) Name(string) string  { return "x" } // takes an arg
func (hostilePlugin) Description() string { return "ok" }

func TestCallStringRejectsWrongSignature(t *testing.T) {
	v := reflect.ValueOf(hostilePlugin{})
	if got := callString(v, "Name"); got != "" {
		t.Fatalf("callString must not call a method that takes arguments, got %q", got)
	}
	if got := callString(v, "Description"); got != "ok" {
		t.Fatalf("callString should call a valid method, got %q", got)
	}
	if got := callString(v, "Missing"); got != "" {
		t.Fatalf("callString on a missing method should be empty, got %q", got)
	}
}

// A method that panics internally must not crash the inspector.
type panicPlugin struct{}

func (panicPlugin) Name() string { panic("boom") }

func TestCallStringRecoversFromPanic(t *testing.T) {
	v := reflect.ValueOf(panicPlugin{})
	if got := callString(v, "Name"); got != "" {
		t.Fatalf("callString should recover and return empty, got %q", got)
	}
}

// Interval with a wrong signature must not be called.
type hostileInterval struct{}

func (hostileInterval) Interval(int) time.Duration { return time.Second }
func (hostileInterval) Good()                      {}

func TestCallDurationSecRejectsWrongSignature(t *testing.T) {
	v := reflect.ValueOf(hostileInterval{})
	if got := callDurationSec(v, "Interval"); got != 0 {
		t.Fatalf("callDurationSec must not call a method with args, got %v", got)
	}
	if got := callDurationSec(v, "Missing"); got != 0 {
		t.Fatalf("callDurationSec on missing method should be 0, got %v", got)
	}
}

type durPlugin struct{}

func (durPlugin) Interval() time.Duration { return 90 * time.Second }

func TestCallDurationSecValid(t *testing.T) {
	v := reflect.ValueOf(durPlugin{})
	if got := callDurationSec(v, "Interval"); got != 90 {
		t.Fatalf("callDurationSec = %v, want 90", got)
	}
}
