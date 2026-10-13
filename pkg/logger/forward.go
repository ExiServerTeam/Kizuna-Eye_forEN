package logger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// forwardQueueSize bounds the in-memory backlog of a log forwarder. A slow or
// unreachable collector can therefore never grow memory without bound, and the
// application's logging path is never blocked by the network.
const forwardQueueSize = 256

// forwardCloseGrace bounds how long Close waits for the in-flight line to be
// sent, so shutdown is not delayed by a dead collector.
const forwardCloseGrace = 2 * time.Second

// forwardWriter mirrors log lines to a remote HTTP(S) endpoint. It is opt-in
// and never blocks the caller: when the bounded queue is full the line is
// dropped (and counted) rather than stalling logging. Redirects are NOT
// followed: a compromised collector must not be able to redirect the stream of
// (potentially sensitive) log lines to another destination.
//
// pkg/logger is host-only (no plugin imports it), so adding this file does not
// change any plugin's build hash and does not force a plugin rebuild.
type forwardWriter struct {
	url    string
	client *http.Client
	ch     chan []byte
	done   chan struct{}

	closeOnce sync.Once
	closed    atomic.Bool
	dropped   atomic.Uint64
}

// newForwardWriter validates rawURL and starts the background sender. An
// invalid URL (wrong scheme, no host) is an error so the caller can log a
// clear warning instead of silently forwarding nowhere.
func newForwardWriter(rawURL string, timeout time.Duration) (*forwardWriter, error) {
	s := strings.TrimSpace(rawURL)
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("log_forward.url を解析できません: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("log_forward.url は http/https のみ: %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("log_forward.url にホストがありません")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	fw := &forwardWriter{
		url:  s,
		ch:   make(chan []byte, forwardQueueSize),
		done: make(chan struct{}),
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	go fw.run()
	return fw, nil
}

func (fw *forwardWriter) run() {
	for line := range fw.ch {
		fw.post(line)
	}
	close(fw.done)
}

func (fw *forwardWriter) post(line []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), fw.client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fw.url, bytes.NewReader(line))
	if err != nil {
		fw.dropped.Add(1)
		return
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := fw.client.Do(req)
	if err != nil {
		fw.dropped.Add(1)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		fw.dropped.Add(1)
	}
}

// Write enqueues a copy of p (the caller's buffer is reused) and always
// returns success so a forward failure can never break file/stdout logging.
func (fw *forwardWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Refuse writes after Close: the sender channel is closed then, and a send
	// would panic. The logger already serializes Write/Close under its own
	// mutex, but this keeps the writer safe on its own (a future caller could
	// Close and keep logging).
	if fw.closed.Load() {
		return len(p), nil
	}
	buf := make([]byte, len(p))
	copy(buf, p)
	select {
	case fw.ch <- buf:
	default:
		fw.dropped.Add(1)
	}
	return len(p), nil
}

// Dropped reports how many lines could not be forwarded (queue full or send
// error). It is used by tests; production callers do not need it.
func (fw *forwardWriter) Dropped() uint64 { return fw.dropped.Load() }

// Close stops the sender and waits (bounded) for the in-flight line.
func (fw *forwardWriter) Close() error {
	fw.closeOnce.Do(func() {
		fw.closed.Store(true)
		close(fw.ch)
	})
	select {
	case <-fw.done:
	case <-time.After(forwardCloseGrace):
	}
	return nil
}
