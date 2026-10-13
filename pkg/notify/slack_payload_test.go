package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Slack payload must not let alert content create live mentions or link
// previews.
func TestSlackPayloadGuardsMentions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if atts, ok := body["attachments"].([]interface{}); ok && len(atts) > 0 {
			if att, ok := atts[0].(map[string]interface{}); ok {
				if mk, ok := att["mrkdwn_in"].([]interface{}); !ok || len(mk) != 0 {
					t.Errorf("mrkdwn_in should be empty, got %v", att["mrkdwn_in"])
				}
			}
		}
		if body["unfurl_links"] != false {
			t.Errorf("unfurl_links should be false, got %v", body["unfurl_links"])
		}
		if text, ok := body["text"].(string); ok {
			if strings.Contains(text, "<!channel>") || strings.Contains(text, "<@") {
				t.Errorf("text contains a live mention marker: %q", text)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewSlackNotifier(srv.URL)
	err := n.Send(context.Background(), &Alert{
		Type:      "security_ssh_failed",
		Level:     LevelWarning,
		Icon:      "⚠️",
		Title:     "SSH <!channel> failure",
		Message:   "user <@U123> from <http://evil|link>",
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// Discord must set allowed_mentions parse=[] so content cannot ping users.
func TestDiscordPayloadDisallowsMentions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		am, ok := body["allowed_mentions"].(map[string]interface{})
		if !ok {
			t.Fatalf("allowed_mentions missing: %v", body)
		}
		parse, ok := am["parse"].([]interface{})
		if !ok || len(parse) != 0 {
			t.Errorf("allowed_mentions.parse should be empty, got %v", am["parse"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewDiscordNotifier(srv.URL)
	err := n.Send(context.Background(), &Alert{
		Type:      "memory",
		Level:     LevelCritical,
		Title:     "@everyone test",
		Message:   "body",
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}
