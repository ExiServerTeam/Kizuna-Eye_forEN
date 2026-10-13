package logger

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestForwardWriterRejectsBadURL: a non-http scheme / missing host is an error.
func TestForwardWriterRejectsBadURL(t *testing.T) {
	for _, u := range []string{"ftp://x/y", "not a url", "http://"} {
		if _, err := newForwardWriter(u, time.Second); err == nil {
			t.Fatalf("newForwardWriter(%q) should fail", u)
		}
	}
}

// TestForwardWriterDelivers: a line written to the logger reaches the endpoint.
func TestForwardWriterDelivers(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		mu.Lock()
		got = append(got, string(buf[:n]))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	fw, err := newForwardWriter(srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("newForwardWriter: %v", err)
	}
	defer fw.Close()

	if _, err := fw.Write([]byte("hello-line\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 || !strings.Contains(got[0], "hello-line") {
		t.Fatalf("endpoint did not receive the line, got=%v", got)
	}
}

// TestForwardWriterDoesNotFollowRedirect: a 3xx must NOT be followed (a
// compromised collector must not redirect the log stream elsewhere).
func TestForwardWriterDoesNotFollowRedirect(t *testing.T) {
	var mu sync.Mutex
	redirected := false
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		redirected = true
		mu.Unlock()
	}))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL, http.StatusFound)
	}))
	defer src.Close()

	fw, err := newForwardWriter(src.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("newForwardWriter: %v", err)
	}
	defer fw.Close()
	_, _ = fw.Write([]byte("x\n"))

	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if redirected {
		t.Fatal("forwarder followed a redirect; it must refuse (http.ErrUseLastResponse)")
	}
}

// TestForwardWriterNonBlocking: a dead endpoint must not block Write and the
// queue must stay bounded (lines are dropped, not buffered forever).
func TestForwardWriterNonBlocking(t *testing.T) {
	// A server that never responds; requests will time out.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()

	fw, err := newForwardWriter(srv.URL, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("newForwardWriter: %v", err)
	}
	defer fw.Close()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			_, _ = fw.Write([]byte("line\n"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Write blocked on a dead endpoint")
	}
	if fw.Dropped() == 0 {
		t.Fatal("expected some lines to be dropped with a dead endpoint")
	}
}

// TestForwardWriterConcurrentCloseWrite: Write racing with Close must not
// panic (send on a closed channel). The closed-flag-only version had a TOCTOU
// window between the check and the send; run with -race and many iterations to
// exercise the interleaving.
func TestForwardWriterConcurrentCloseWrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	for i := 0; i < 200; i++ {
		fw, err := newForwardWriter(srv.URL, 50*time.Millisecond)
		if err != nil {
			t.Fatalf("newForwardWriter: %v", err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = fw.Write([]byte("line\n"))
			}
		}()
		go func() {
			defer wg.Done()
			_ = fw.Close()
		}()
		// A panic in either goroutine crashes the test process; the race
		// detector also flags the unsynchronized close/send.
		wg.Wait()
	}
}

// TestForwardWriterWriteAfterClose: Write after Close must not panic
// (send on a closed channel).
func TestForwardWriterWriteAfterClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	fw, err := newForwardWriter(srv.URL, time.Second)
	if err != nil {
		t.Fatalf("newForwardWriter: %v", err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Must not panic and must report success (drop after close).
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Write after Close panicked: %v", r)
		}
	}()
	if _, err := fw.Write([]byte("after-close\n")); err != nil {
		t.Fatalf("Write after Close returned error: %v", err)
	}
}
