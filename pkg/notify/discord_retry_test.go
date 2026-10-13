package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestDiscordRetryOn429 verifies that a 429 response is retried and the send
// eventually succeeds. This is the core fix for the attack test where 69% of
// notifications were dropped with no retry.
func TestDiscordRetryOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := NewDiscordNotifierWithOptions(srv.URL, 5, 1, nil)
	err := d.Send(context.Background(), &Alert{Type: "t", Level: LevelWarning, Title: "x", Message: "y", Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3 (2 failures + 1 success)", got)
	}
}

// TestDiscordGivesUpAfterMaxRetries verifies the retry cap is honored.
func TestDiscordGivesUpAfterMaxRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	d := NewDiscordNotifierWithOptions(srv.URL, 3, 1, nil)
	err := d.Send(context.Background(), &Alert{Type: "t", Level: LevelWarning, Title: "x", Message: "y", Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected failure after exhausting retries")
	}
	// 1 initial + 3 retries = 4 total attempts
	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Fatalf("calls = %d, want 4", got)
	}
}

// TestDiscordNoRetryOn4xx verifies a non-429 4xx is not retried (bad payload
// is permanent; retrying would just waste the retry budget).
func TestDiscordNoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	d := NewDiscordNotifierWithOptions(srv.URL, 5, 1, nil)
	err := d.Send(context.Background(), &Alert{Type: "t", Level: LevelWarning, Title: "x", Message: "y", Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected failure")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 400)", got)
	}
}

// TestDiscordNoRetryWhenDisabled verifies maxRetries=0 keeps old behaviour.
func TestDiscordNoRetryWhenDisabled(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	d := NewDiscordNotifierWithOptions(srv.URL, 0, 1, nil)
	_ = d.Send(context.Background(), &Alert{Type: "t", Level: LevelWarning, Title: "x", Message: "y", Timestamp: time.Now()})
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1 (retry disabled)", got)
	}
}

// TestParseRetryAfter covers seconds and HTTP-date forms.
func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("5"); got != 5*time.Second {
		t.Errorf("seconds: got %s", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty: got %s", got)
	}
	if got := parseRetryAfter("garbage"); got != 0 {
		t.Errorf("invalid: got %s", got)
	}
	future := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 4*time.Second {
		t.Errorf("http-date: got %s", got)
	}
}
