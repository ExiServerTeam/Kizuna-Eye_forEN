package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMeta writes a meta.json for name with the given origin and entry.
func writeMeta(t *testing.T, dir, name, origin, entry string) {
	t.Helper()
	body := `{"name":"` + name + `","origin":"` + origin + `","web_ui_path":"` + entry + `"}`
	if err := os.WriteFile(filepath.Join(dir, name+".meta.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The wrapper must point the iframe at the module's declared entry, not a
// hardcoded index.html. Design 5.4: the module owns its UI.
func TestWebUIThirdPartyWrapperUsesDeclaredEntry(t *testing.T) {
	dir := t.TempDir()
	webDir := filepath.Join(dir, "kizuna_demo.web")
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "card.html"), []byte("<html>card</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMeta(t, dir, "kizuna_demo", "third_party", "card.html")

	p := &PluginManager{pluginsDir: dir}
	rec := httptest.NewRecorder()
	p.handleWebUI(rec, webUIRequest("kizuna_demo", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/plugins/kizuna_demo/_asset/card.html") {
		t.Fatalf("wrapper must embed the declared entry: %s", body)
	}
	// The declared entry must actually resolve (rejects the old 404).
	rec2 := httptest.NewRecorder()
	p.handleWebUI(rec2, webUIRequest("kizuna_demo", "_asset/card.html"))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "card</html>") {
		t.Fatalf("declared entry must be served, got %d %q", rec2.Code, rec2.Body.String())
	}
}

// A hostile entry that tries to escape the UI root must fall back to
// index.html (fail-closed), never emit a traversal URL.
func TestWebUIWrapperRejectsTraversalEntry(t *testing.T) {
	for _, entry := range []string{"../secret.html", "/etc/passwd", `a\\b.html`} {
		dir := t.TempDir()
		webDir := filepath.Join(dir, "kizuna_demo.web")
		if err := os.MkdirAll(webDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeMeta(t, dir, "kizuna_demo", "third_party", entry)

		p := &PluginManager{pluginsDir: dir}
		rec := httptest.NewRecorder()
		p.handleWebUI(rec, webUIRequest("kizuna_demo", ""))
		body := rec.Body.String()
		if strings.Contains(body, "..") || strings.Contains(body, "/etc/") {
			t.Fatalf("entry %q must not appear in the wrapper: %s", entry, body)
		}
		if !strings.Contains(body, "/_asset/index.html") {
			t.Fatalf("entry %q must fall back to index.html: %s", entry, body)
		}
	}
}

// The wrapper must forward the dashboard's ?lang= to the inner iframe (only
// ja/en accepted) so a third-party card renders in the current language.
func TestWebUIWrapperForwardsLang(t *testing.T) {
	dir := t.TempDir()
	webDir := filepath.Join(dir, "kizuna_demo.web")
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMeta(t, dir, "kizuna_demo", "third_party", "index.html")
	p := &PluginManager{pluginsDir: dir}

	req := httptest.NewRequest(http.MethodGet, "/plugins/kizuna_demo/?lang=en", nil)
	req.SetPathValue("name", "kizuna_demo")
	rec := httptest.NewRecorder()
	p.handleWebUI(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "_asset/index.html?lang=en") {
		t.Fatalf("wrapper must forward lang=en to the inner iframe: %s", body)
	}

	// A hostile lang value must not be reflected (whitelist -> ja).
	req2 := httptest.NewRequest(http.MethodGet, "/plugins/kizuna_demo/?lang=%22%3E%3Cscript%3E", nil)
	req2.SetPathValue("name", "kizuna_demo")
	rec2 := httptest.NewRecorder()
	p.handleWebUI(rec2, req2)
	if b := rec2.Body.String(); strings.Contains(b, "<script>") || strings.Contains(b, "lang=\"\"") {
		t.Fatalf("hostile lang must not be reflected: %s", b)
	}
}

// safeWebUIEntry defaults to index.html when meta is missing or empty.
func TestSafeWebUIEntryDefault(t *testing.T) {
	dir := t.TempDir()
	p := &PluginManager{pluginsDir: dir}
	if got := p.safeWebUIEntry("nope"); got != "index.html" {
		t.Fatalf("missing meta: got %q, want index.html", got)
	}
	writeMeta(t, dir, "empty", "third_party", "")
	if got := p.safeWebUIEntry("empty"); got != "index.html" {
		t.Fatalf("empty entry: got %q, want index.html", got)
	}
}
