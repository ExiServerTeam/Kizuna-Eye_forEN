package logger

import "testing"

// TestCatalogLanguagesHaveSameKeys pins a property that was previously only
// checked by an external node script (scripts/check_i18n.js, which also covers
// the web UI). The Go-side catalog is the one that feeds the JSON Lines log
// entries, so a key added to only one language silently degrades the other
// language to the raw key (msg() falls back to catalog["ja"], which is empty
// for an en-only key).
//
// This is a Go test on purpose: it runs in CI with the rest of the suite and
// does not depend on Node or a workspace path.
func TestCatalogLanguagesHaveSameKeys(t *testing.T) {
	ja, ok := catalog["ja"]
	if !ok {
		t.Fatal("catalog[ja] is missing")
	}
	en, ok := catalog["en"]
	if !ok {
		t.Fatal("catalog[en] is missing")
	}

	for k := range ja {
		if _, ok := en[k]; !ok {
			t.Errorf("key %q exists in ja but not en", k)
		}
	}
	for k := range en {
		if _, ok := ja[k]; !ok {
			t.Errorf("key %q exists in en but not ja", k)
		}
	}
}

// TestCatalogValuesAreNonEmpty guards against a placeholder key that renders
// as an empty string (which would produce a blank log line).
func TestCatalogValuesAreNonEmpty(t *testing.T) {
	for lang, table := range catalog {
		for k, v := range table {
			if v == "" {
				t.Errorf("catalog[%s][%q] is empty", lang, k)
			}
		}
	}
}
