package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newWebUITestManager creates a PluginManager with a .web dir for name and a
// meta.json whose origin is set. It bypasses NewPluginManager (which requires
// plugin-inspect) so the web UI handler can be tested in isolation.
func newWebUITestManager(t *testing.T, name, origin string, files map[string]string) *PluginManager {
	t.Helper()
	dir := t.TempDir()
	webDir := filepath.Join(dir, name+".web")
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(webDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	meta := `{"name":"` + name + `","origin":"` + origin + `"}`
	if err := os.WriteFile(filepath.Join(dir, name+".meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return &PluginManager{pluginsDir: dir}
}

func webUIRequest(name, sub string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/plugins/"+name+"/"+sub, nil)
	req.SetPathValue("name", name)
	return req
}

// A third-party plugin's web UI must be served through the sandbox wrapper
// (design 5.4), not directly. The wrapper references the _asset/ path and the
// iframe has no allow-same-origin.
func TestWebUIThirdPartySandboxWrapper(t *testing.T) {
	p := newWebUITestManager(t, "kizuna_demo", "third_party", map[string]string{
		"index.html": "<html>hi</html>",
	})
	rec := httptest.NewRecorder()
	p.handleWebUI(rec, webUIRequest("kizuna_demo", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<iframe") || !strings.Contains(body, "sandbox=") {
		t.Fatalf("third-party UI must be wrapped in a sandboxed iframe: %s", body)
	}
	if strings.Contains(body, "allow-same-origin") {
		t.Fatal("sandbox must not include allow-same-origin (opaque origin required)")
	}
	if !strings.Contains(body, "/plugins/kizuna_demo/_asset/index.html") {
		t.Fatalf("wrapper must reference the _asset path: %s", body)
	}
	if strings.Contains(body, "hi</html>") {
		t.Fatal("wrapper must not inline the plugin HTML directly")
	}
}

// The _asset path for a third-party plugin must be served with a CSP that
// forbids network access (connect-src 'none'), so the isolated UI cannot call
// the dashboard API.
func TestWebUIThirdPartyAssetCSP(t *testing.T) {
	p := newWebUITestManager(t, "kizuna_demo", "third_party", map[string]string{
		"_asset/index.html": "<html>asset</html>",
	})
	// ラッパーは _asset/index.html を参照する。ServeFile は "/index.html" を
	// ディレクトリ "/" へ 301 リダイレクトする（ブラウザが追従する）ため、
	// ここではリダイレクト先の _asset/ を要求して 200 と CSP を確認する。
	rec := httptest.NewRecorder()
	p.handleWebUI(rec, webUIRequest("kizuna_demo", "_asset/"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("third-party asset CSP must forbid connect-src: %q", csp)
	}
	// The asset is embedded in the wrapper's sandbox iframe, so it must NOT
	// carry X-Frame-Options: DENY (which would block the iframe). SAMEORIGIN
	// allows the same-origin wrapper to embed it.
	if xfo := rec.Header().Get("X-Frame-Options"); xfo == "DENY" {
		t.Fatalf("third-party asset must not be X-Frame-Options: DENY (blocks iframe): %q", xfo)
	}
	if !strings.Contains(rec.Body.String(), "asset</html>") {
		t.Fatal("asset content should be served")
	}
}

// fail-closed: a .web dir with NO readable meta.json must be treated as
// third-party (sandboxed), so a dropped meta.json cannot serve an
// unsandboxed UI that can call the dashboard API.
func TestWebUINoMetaIsSandboxed(t *testing.T) {
	dir := t.TempDir()
	webDir := filepath.Join(dir, "kizuna_nometa.web")
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html>x</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Intentionally no <name>.meta.json.
	p := &PluginManager{pluginsDir: dir}

	rec := httptest.NewRecorder()
	p.handleWebUI(rec, webUIRequest("kizuna_nometa", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<iframe") {
		t.Fatalf("missing meta must fall back to the sandbox wrapper: %s", rec.Body.String())
	}
}

// An official plugin's web UI is served directly (no sandbox wrapper) and its
// CSP allows scripts from 'self' only.
func TestWebUIOfficialDirect(t *testing.T) {
	p := newWebUITestManager(t, "kizuna_official", "official", map[string]string{
		"index.html": "<html>official</html>",
	})
	rec := httptest.NewRecorder()
	p.handleWebUI(rec, webUIRequest("kizuna_official", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<iframe") {
		t.Fatal("official UI must be served directly, not wrapped")
	}
	if !strings.Contains(body, "official</html>") {
		t.Fatalf("official index.html should be served: %s", body)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "'unsafe-inline'") && strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("official CSP should restrict scripts to 'self': %q", csp)
	}
}
