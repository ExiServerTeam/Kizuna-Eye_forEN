package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A state-changing request from a different host must be rejected (CSRF).
func TestMiddlewareRejectsCrossOriginPost(t *testing.T) {
	m, _ := newTestMiddleware(t, false, "")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", nil)
	req.Host = "dashboard.local"
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	m.Wrap(next).ServeHTTP(rec, req)

	if called {
		t.Fatal("cross-origin POST must not reach the handler")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// A same-origin POST is allowed.
func TestMiddlewareAllowsSameOriginPost(t *testing.T) {
	m, _ := newTestMiddleware(t, false, "")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/api/modules", nil)
	req.Host = "dashboard.local"
	req.Header.Set("Origin", "http://dashboard.local")
	m.Wrap(next).ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Fatal("same-origin POST should reach the handler")
	}
}

// A POST without an Origin header (curl / script) is allowed.
func TestMiddlewareAllowsPostWithoutOrigin(t *testing.T) {
	m, _ := newTestMiddleware(t, false, "")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/api/modules", nil)
	m.Wrap(next).ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Fatal("POST without Origin should reach the handler")
	}
}

// Behind a loopback reverse proxy, X-Forwarded-Host is honored so a
// same-origin POST does not get a false 403 when the proxy rewrites Host.
func TestMiddlewareTrustsXForwardedHostFromLoopback(t *testing.T) {
	m, _ := newTestMiddleware(t, false, "")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/api/modules", nil)
	req.RemoteAddr = "127.0.0.1:5555" // local proxy
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "https://eye.example.com")
	req.Header.Set("X-Forwarded-Host", "eye.example.com")
	m.Wrap(next).ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Fatal("X-Forwarded-Host from loopback proxy should be trusted")
	}
}

// A non-loopback peer must NOT be able to spoof X-Forwarded-Host to bypass
// the CSRF check.
func TestMiddlewareIgnoresXForwardedHostFromRemote(t *testing.T) {
	m, _ := newTestMiddleware(t, false, "")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/api/modules", nil)
	req.RemoteAddr = "203.0.113.9:4444" // remote client
	req.Host = "dashboard.local"
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	rec := httptest.NewRecorder()
	m.Wrap(next).ServeHTTP(rec, req)

	if called {
		t.Fatal("remote client must not spoof X-Forwarded-Host")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// With auth disabled, the plugin upload endpoint must be refused (RCE vector).
func TestMiddlewareBlocksUploadWhenAuthDisabled(t *testing.T) {
	m, _ := newTestMiddleware(t, false, "")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", nil)
	rec := httptest.NewRecorder()
	m.Wrap(next).ServeHTTP(rec, req)

	if called {
		t.Fatal("plugin upload must not reach the handler when auth is disabled")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// Other POSTs still pass when auth is disabled (trusted-network mode).
func TestMiddlewareAllowsModulesWhenAuthDisabled(t *testing.T) {
	m, _ := newTestMiddleware(t, false, "")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodPost, "/api/modules", nil)
	m.Wrap(next).ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Fatal("non-upload POST should pass when auth is disabled")
	}
}
