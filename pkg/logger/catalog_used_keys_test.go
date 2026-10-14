package logger

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// keyCallRe matches a catalog key passed to a *T logging method, e.g.
//   lg.InfoT("dash.api_insight")   /   l.WarnT("agent.dep_missing", ...)
// The receiver can be any identifier, so the regex only anchors on the method
// name and the string literal.
var keyCallRe = regexp.MustCompile(`\.(?:Info|Warn|Error|Debug|Fatal)T\("([a-z0-9_.]+)"`)

// TestCatalogCoversAllUsedKeys scans the Go sources for catalog keys passed to
// the *T logging methods and fails if any key is missing from the catalog.
//
// TestCatalogLanguagesHaveSameKeys only checks that ja and en agree; it cannot
// catch a key that is used in code but defined in neither language. That is
// exactly how "dash.api_insight" shipped as a raw key in the log output (the
// log fell back to the key literal). This test closes that gap.
func TestCatalogCoversAllUsedKeys(t *testing.T) {
	// The test runs in pkg/logger, so the repository root is two levels up.
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("cannot locate repository root from %q: %v", root, err)
	}

	ja, ok := catalog["ja"]
	if !ok {
		t.Fatal("catalog[ja] is missing")
	}

	used := map[string][]string{} // key -> files that use it

	scanDir := func(base string) {
		err := filepath.WalkDir(filepath.Join(root, base), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Skip VCS/editor and generated directories.
				switch d.Name() {
				case ".git", "node_modules", "dist", "apt-repo", "tmp":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range keyCallRe.FindAllStringSubmatch(string(data), -1) {
				used[m[1]] = append(used[m[1]], path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", base, err)
		}
	}

	for _, base := range []string{"cmd", "internal", "pkg"} {
		scanDir(base)
	}

	for key, files := range used {
		if _, ok := ja[key]; !ok {
			t.Errorf("log key %q is used but missing from the catalog (used in: %s)", key, strings.Join(files, ", "))
		}
	}
}
