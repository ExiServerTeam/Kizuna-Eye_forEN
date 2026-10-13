package main

import (
	"net/http"
)

// bodyLimitMiddleware はリクエスト本文のサイズを制限し、過大な JSON や
// multipart アップロードによるメモリ/ディスク枯渇を防ぐ。
//
// JSON API リクエストは jsonLimitBytes に制限し、プラグインアップロード
// エンドポイントは .so バイナリを運ぶため uploadLimitBytes を与える。
// bodyLimitMiddleware caps the size of request bodies to protect against
// memory/disk exhaustion from oversized JSON or multipart uploads.
//
// A JSON API request is limited to jsonLimitBytes; the plugin upload
// endpoint gets uploadLimitBytes because it carries a .so binary.
const (
	jsonLimitBytes   = 1 << 20  // 1 MiB
	uploadLimitBytes = 64 << 20 // 64 MiB
)

func bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(jsonLimitBytes)
		if r.Method == http.MethodPost && r.URL.Path == "/api/plugins/upload" {
			limit = int64(uploadLimitBytes)
		}
		// アバターアップロードは multipart 画像（最大 1 MiB）なので、
		// multipart のフレーミング分だけ JSON 上限より少し多く許可する。
		// Avatar upload is a multipart image (max 1 MiB), so allow a little
		// more than the JSON limit for the multipart framing.
		if r.Method == http.MethodPost && r.URL.Path == "/api/auth/avatar" {
			limit = int64(2 << 20) // 2 MiB
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
