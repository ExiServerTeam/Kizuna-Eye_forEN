package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// G-4: 認証無効時でも、パスを正規化してからアップロード API と判定すること。
// /api/plugins/x/../upload のような別名で「認証が必要」の拒否を回避できない。
func TestMiddlewareBlocksUploadAliasWhenAuthDisabled(t *testing.T) {
	m, _ := newTestMiddleware(t, false, "")
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	r := httptest.NewRequest(http.MethodPost, "/api/plugins/x/../upload", nil)
	rec := httptest.NewRecorder()
	m.Wrap(next).ServeHTTP(rec, r)

	if called || rec.Code != http.StatusForbidden {
		t.Fatalf("upload alias: called=%v code=%d, want blocked with 403", called, rec.Code)
	}
}

// G-4: 正規化しても要求ロールが下がらないこと（別名で緩いルールにマッチ
// しない）。Wrap は canonicalPath を通したパスでルール照合する。
func TestMinRoleForMatchesCanonicalPath(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want string
	}{
		{"/api/plugins/x/../upload", "/api/plugins/upload"},
		{"/api/logs/../config", "/api/config"},
		{"/api/./plugins/upload", "/api/plugins/upload"},
	} {
		canonical := canonicalPath(c.raw)
		if canonical != c.want {
			t.Fatalf("canonicalPath(%q) = %q, want %q", c.raw, canonical, c.want)
		}
		rawMin, _ := minRoleFor(http.MethodPost, c.raw)
		canMin, canOK := minRoleFor(http.MethodPost, canonical)
		if canOK && !canMin.AtLeast(rawMin) {
			t.Fatalf("canonical %q requires %v, which is weaker than the alias rule %v",
				canonical, canMin, rawMin)
		}
	}
	if min, ok := minRoleFor(http.MethodPost, canonicalPath("/api/plugins/x/../upload")); !ok || !min.AtLeast(RoleAdmin) {
		t.Fatalf("upload alias must resolve to least admin, got (%v,%v)", min, ok)
	}
}

// G-5: 出所不明のログイン試行が大量に来ても、IP ごとの記録が無制限に
// 増えないこと（メモリ枯渇対策）。
func TestLoginLimiterBoundsMapSize(t *testing.T) {
	l := newLoginLimiter(3, time.Minute, time.Minute)
	defer l.Stop()

	for i := 0; i <= maxLimiterEntries+1; i++ {
		l.RecordFailure(fmt.Sprintf("10.%d.%d.%d", i/(256*256)%256, i/256%256, i%256))
	}

	l.mu.Lock()
	n := len(l.attempts)
	l.mu.Unlock()
	if n > maxLimiterEntries {
		t.Fatalf("attempts map grew to %d entries, want <= %d", n, maxLimiterEntries)
	}
}
