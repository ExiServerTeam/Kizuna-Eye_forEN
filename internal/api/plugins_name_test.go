package api

import "testing"

// Regression: a plugin self-reported Name() that is not filesystem-safe must
// be rejected by sanitizeName so it never reaches meta.json or a path join.
func TestSanitizeNameRejectsUnsafe(t *testing.T) {
	unsafe := []string{
		"../../etc/passwd",
		"a/b",
		"a\\b",
		"..",
		"",
		"has space",
	}
	for _, name := range unsafe {
		if _, err := sanitizeName(name); err == nil {
			t.Errorf("sanitizeName(%q) should fail", name)
		}
	}

	safe := []string{"demo", "my-plugin_1"}
	for _, name := range safe {
		if _, err := sanitizeName(name); err != nil {
			t.Errorf("sanitizeName(%q) should succeed: %v", name, err)
		}
	}
}
