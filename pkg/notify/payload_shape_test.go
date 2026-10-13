package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// These tests pin the wire format of the Discord / LINE notifiers. If the
// upstream Webhook API changes (or a refactor renames a field), the JSON shape
// asserted here fails, so the breakage is caught in CI rather than in
// production where a silently dropped notification is invisible.

// Discord: the payload must be a single embed with the expected fields, and
// mentions must be disabled.
func TestDiscordPayloadShape(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewDiscordNotifier(srv.URL)
	err := n.Send(context.Background(), &Alert{
		Type: "memory", Level: LevelCritical, Icon: "🚨",
		Title: "Memory", Message: "high", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	embeds, ok := got["embeds"].([]interface{})
	if !ok || len(embeds) != 1 {
		t.Fatalf("embeds = %v, want a single-element array", got["embeds"])
	}
	embed, _ := embeds[0].(map[string]interface{})
	for _, k := range []string{"title", "description", "color", "timestamp", "footer"} {
		if _, ok := embed[k]; !ok {
			t.Errorf("embed missing %q: %v", k, embed)
		}
	}
	// allowed_mentions.parse must be an empty array (no @everyone/roles).
	am, ok := got["allowed_mentions"].(map[string]interface{})
	if !ok {
		t.Fatalf("allowed_mentions missing: %v", got)
	}
	if parse, ok := am["parse"].([]interface{}); !ok || len(parse) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want empty array", am["parse"])
	}
}

// LINE: the body must be form-encoded with a single "message" field and the
// bearer token must be sent.
func TestLINEPayloadShape(t *testing.T) {
	var gotBody string
	var gotAuth string
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		if b, err := io.ReadAll(r.Body); err == nil {
			gotBody = string(b)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	old := lineAPIURL
	lineAPIURL = srv.URL
	defer func() { lineAPIURL = old }()

	n := NewLINENotifier("secret-token")
	err := n.Send(context.Background(), &Alert{
		Type: "disk", Level: LevelWarning, Title: "Disk", Message: "low", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want form-urlencoded", gotCT)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want Bearer secret-token", gotAuth)
	}
	vals, err := url.ParseQuery(gotBody)
	if err != nil {
		t.Fatalf("body is not form-encoded: %v (%q)", err, gotBody)
	}
	if vals.Get("message") == "" {
		t.Errorf("message field missing/empty: %v", vals)
	}
	if len(vals) != 1 {
		t.Errorf("expected only a message field, got %v", vals)
	}
}
