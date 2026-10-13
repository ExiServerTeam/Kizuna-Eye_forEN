package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A third-party plugin's asset requested via /_asset/<rel> must be resolved to
// the file extracted at baseDir/<rel> (design 5.4: the sandbox wrapper loads
// the module's real UI through _asset/). Regression: the wrapper referenced
// _asset/ but the files were extracted without that prefix, so every request
// 404'd and no third-party UI ever rendered.
func TestWebUIThirdPartyAssetResolves(t *testing.T) {
	p := newWebUITestManager(t, "kizuna_demo", "third_party", map[string]string{
		"index.html": "<html>root</html>",
		"style.css":  "body{}",
	})

	for req, want := range map[string]string{
		"_asset/index.html": "<html>root</html>",
		"_asset/style.css":  "body{}",
	} {
		rec := httptest.NewRecorder()
		p.handleWebUI(rec, webUIRequest("kizuna_demo", req))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", req, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("GET %s: body %q does not contain %q", req, rec.Body.String(), want)
		}
	}
}

// The wrapper must be returned for the bare path AND for a direct
// /index.html request, so the real UI is only reachable through the sandboxed
// iframe (its opaque origin cannot call the dashboard API).
func TestWebUIThirdPartyDirectPathIsWrapped(t *testing.T) {
	p := newWebUITestManager(t, "kizuna_demo", "third_party", map[string]string{
		"index.html": "<html>root</html>",
	})

	for _, req := range []string{"", "index.html"} {
		rec := httptest.NewRecorder()
		p.handleWebUI(rec, webUIRequest("kizuna_demo", req))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %q: status = %d, want 200", req, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "<iframe") || !strings.Contains(body, "sandbox=") {
			t.Fatalf("GET %q: expected the sandbox wrapper, got %s", req, body)
		}
		if strings.Contains(body, "root</html>") {
			t.Fatalf("GET %q: the real UI must not be served directly", req)
		}
	}
}

// A CSS asset must be served with a CSS content type (not text/html), or
// nosniff makes the browser refuse to apply it.
func TestWebUIThirdPartyAssetContentType(t *testing.T) {
	p := newWebUITestManager(t, "kizuna_demo", "third_party", map[string]string{
		"index.html": "<html>root</html>",
		"style.css":  "body{}",
	})
	rec := httptest.NewRecorder()
	p.handleWebUI(rec, webUIRequest("kizuna_demo", "_asset/style.css"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Fatalf("style.css Content-Type = %q, want text/css", ct)
	}
}

// Path traversal through the _asset/ prefix must not escape the UI directory.
func TestWebUIThirdPartyAssetTraversalRejected(t *testing.T) {
	p := newWebUITestManager(t, "kizuna_demo", "third_party", map[string]string{
		"index.html": "<html>root</html>",
	})

	rec := httptest.NewRecorder()
	p.handleWebUI(rec, webUIRequest("kizuna_demo", "_asset/../../etc/passwd"))
	if rec.Code == http.StatusOK {
		t.Fatalf("traversal must not be served (status=%d body=%s)", rec.Code, rec.Body.String())
	}
}
