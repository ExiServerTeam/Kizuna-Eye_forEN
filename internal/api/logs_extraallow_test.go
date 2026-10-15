package api

import (
	"os"
	"path/filepath"
	"testing"
)

// M-2: 追加ログ（log_path）は許可プレフィックス配下のみ許可する。
// M-2: an extra log (log_path) is allowed only under an allowed prefix.
func TestExtraLogAllowlist(t *testing.T) {
	root := t.TempDir()
	allowedDir := filepath.Join(root, "data")
	outsideDir := filepath.Join(root, "etc")
	if err := os.MkdirAll(allowedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}

	inside := filepath.Join(allowedDir, "plugin.log")
	outside := filepath.Join(outsideDir, "secret.log")
	for _, p := range []string{inside, outside} {
		if err := os.WriteFile(p, []byte("line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	l := NewLogHandler("", "", "")
	l.SetExtraLogs(map[string]string{"ok": inside, "bad": outside})
	l.SetExtraLogAllowedPrefixes(allowedDir)

	if _, ok := l.resolveLogPath("ok"); !ok {
		t.Error("a log under the allowed prefix must resolve")
	}
	if _, ok := l.resolveLogPath("bad"); ok {
		t.Error("a log outside the allowed prefix must be refused")
	}

	// 許可プレフィックスが未設定なら包含チェックはスキップされる（後方互換）。
	// With no allowlist the containment check is skipped (backward compatible).
	l2 := NewLogHandler("", "", "")
	l2.SetExtraLogs(map[string]string{"bad": outside})
	if _, ok := l2.resolveLogPath("bad"); !ok {
		t.Error("without an allowlist, an extra log should resolve")
	}
}
