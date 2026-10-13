package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Telegram returns HTTP 200 with {"ok":false} on failure; Send must surface it
// as an error instead of reporting success.
func TestTelegramReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	old := telegramAPIBase
	telegramAPIBase = srv.URL
	defer func() { telegramAPIBase = old }()

	n := NewTelegramNotifier("token", "123")
	err := n.Send(context.Background(), &Alert{
		Type: "memory", Level: LevelWarning, Title: "t", Message: "m", Timestamp: time.Now(),
	})
	if err == nil {
		t.Fatal("expected an error when Telegram returns ok:false")
	}
}

// ok:true must be treated as success.
func TestTelegramAcceptsOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	old := telegramAPIBase
	telegramAPIBase = srv.URL
	defer func() { telegramAPIBase = old }()

	n := NewTelegramNotifier("token", "123")
	if err := n.Send(context.Background(), &Alert{Type: "memory", Level: LevelWarning, Timestamp: time.Now()}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// Compile-time guard: the notifier still satisfies Notifier.
var _ Notifier = (*TelegramNotifier)(nil)
