package auth

import (
	"testing"
)

// Regression: a plugin web UI file that ends in a static-asset extension
// (.js/.css/...) must NOT be treated as a public static asset. Before this
// fix, /plugins/foo/app.js matched the static-asset rule in isPublicPath and
// was served without a session, leaking an operator-only plugin UI to
// unauthenticated clients. Only the /plugins/ prefix is gated; ordinary
// static assets (e.g. /app.js) stay public.
func TestIsPublicPathPluginsIsNotPublic(t *testing.T) {
	// Plugin web UI files must not be public, regardless of extension.
	for _, p := range []string{
		"/plugins/foo/app.js",
		"/plugins/foo/style.css",
		"/plugins/foo/logo.png",
		"/plugins/foo/index.html",
	} {
		if isPublicPath(p) {
			t.Errorf("isPublicPath(%q) = true, want false (plugin UI is operator-only)", p)
		}
	}

	// Ordinary static assets must stay public (login page needs its CSS/JS).
	for _, p := range []string{"/app.js", "/style.css", "/i18n.js", "/logo.svg"} {
		if !isPublicPath(p) {
			t.Errorf("isPublicPath(%q) = false, want true (shared static asset)", p)
		}
	}
}
