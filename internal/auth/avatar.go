package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"Kizuna-Eye/pkg/fsutil"
	kelog "Kizuna-Eye/pkg/logger"
)

// maxAvatarBytes はアバター画像アップロードを制限する。
// maxAvatarBytes bounds an avatar image upload.
const maxAvatarBytes = 1 << 20 // 1 MiB

// allowedAvatarExt はスニッフした content type を保持するファイル拡張子へ対応付ける。
// allowedAvatarExt maps a sniffed content type to the file extension we keep.
var allowedAvatarExt = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// avatarDir はアバターファイルを保持するディレクトリを返し、0700 で作成する。
// avatarDir returns the directory that holds avatar files, creating it 0700.
func (h *Handler) avatarDir() (string, error) {
	dir := h.avatarsDir
	if dir == "" {
		dir = "avatars"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// avatarFileName はユーザーのアバター用の安全で一意なファイル名を返す。
// avatarFileName returns a safe, unique file name for a user's avatar.
func avatarFileName(username, ext string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return sanitizeUser(username) + "-" + hex.EncodeToString(b) + ext
}

// sanitizeUser はファイル名に安全な文字だけを保つ。
// sanitizeUser keeps only characters safe for a file name.
func sanitizeUser(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "user"
	}
	return b.String()
}

// handleAvatarUpload は現在のユーザーのアバター画像を保存する。
// handleAvatarUpload stores the current user's avatar image.
func (h *Handler) handleAvatarUpload(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.sessionFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "ログインが必要です")
		return
	}

	if err := r.ParseMultipartForm(maxAvatarBytes + (1 << 20)); err != nil {
		writeError(w, http.StatusBadRequest, "画像の解析に失敗しました: "+err.Error())
		return
	}
	file, _, err := r.FormFile("avatar")
	if err != nil {
		writeError(w, http.StatusBadRequest, "画像がありません: "+err.Error())
		return
	}
	defer file.Close()

	// サイズ超過を検出するため最大 maxAvatarBytes+1 まで読む。
	// Read at most maxAvatarBytes+1 to detect oversize.
	data, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "画像の読み込みに失敗しました")
		return
	}
	if len(data) > maxAvatarBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "画像は1MB以内にしてください")
		return
	}

	// 実 content type をバイト列のスニッフで検証する（拡張子ではない）。
	// Validate the real content type by sniffing the bytes (not the extension).
	ct := http.DetectContentType(data)
	ct = strings.SplitN(ct, ";", 2)[0]
	ext, ok := allowedAvatarExt[strings.TrimSpace(ct)]
	if !ok {
		writeError(w, http.StatusBadRequest, "PNG / JPEG / GIF / WebP のみ対応しています")
		return
	}

	dir, err := h.avatarDir()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存先の作成に失敗しました")
		return
	}

	name := avatarFileName(sess.Username, ext)
	path := filepath.Join(dir, name)
	// fsutil.WriteFileAtomic は symlink ターゲットを拒否し、rename 前に fsync する。
	// ランダムな 16 進 8 バイト接尾辞が既に symlink の事前設置を非現実的にしているが、
	// これによりアバター書き込みが他のすべての状態書き込みと同じアトミックで
	// symlink 安全な経路に乗る（L-12）。
	// fsutil.WriteFileAtomic rejects a symlink target and fsyncs before the
	// rename. The random 16-hex-char suffix already makes a pre-planted
	// symlink infeasible, but this keeps avatar writes on the same atomic,
	// symlink-safe path as every other state write (L-12).
	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, "保存に失敗しました")
		return
	}

	old := h.store.AvatarOf(sess.Username)
	if err := h.store.SetAvatar(sess.Username, name); err != nil {
		_ = os.Remove(path)
		// ゲストセッション（またはユーザーが既に存在しない任意のセッション）は
		// users.json にエントリが無いため SetAvatar が失敗する。これはクライアントの
		// 問題（403）であり、サーバーの障害ではない。500 として表に出さない。
		// A guest session (or any session whose user no longer exists) has
		// no entry in users.json, so SetAvatar fails. That is a client
		// problem (403), not a server fault: do not surface it as 500.
		if errors.Is(err, ErrUserNotFound) {
			writeError(w, http.StatusForbidden, "この操作には実アカウントが必要です")
			return
		}
		writeError(w, http.StatusInternalServerError, "保存に失敗しました")
		return
	}
	// 以前のアバターファイルがあれば除去する。素のファイル名（パス区切りなし）のみを
	// 受け付ける。破損・改ざんされた users.json が "../../etc/passwd" のような値を
	// 保持し得て、それをアバターディレクトリと join すると remove がそれを脱出
	// してしまう。アップロード経路は常にサニタイズ済みの "<user>-<hex>.<ext>" 名を
	// 書くので、正当な値は常に通過する。
	// Remove the previous avatar file, if any. Only a bare file name
	// (no path separators) is accepted: a corrupted or tampered users.json
	// could hold a value such as "../../etc/passwd", and joining that with
	// the avatar directory would make the remove escape it. The upload
	// path always writes a sanitized "<user>-<hex>.<ext>" name, so a
	// legitimate value always passes.
	if old != "" && old != name && safeAvatarBaseName(old) {
		_ = os.Remove(filepath.Join(dir, old))
	}

	if h.logger != nil {
		kelog.LogInfo(h.logger, "auth.avatar_updated", sess.Username)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "avatar": name})
}

// handleAvatarDelete は現在のユーザーのアバターを除去する。
// handleAvatarDelete removes the current user's avatar.
func (h *Handler) handleAvatarDelete(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.sessionFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "ログインが必要です")
		return
	}
	old := h.store.AvatarOf(sess.Username)
	if err := h.store.SetAvatar(sess.Username, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "削除に失敗しました")
		return
	}
	if old != "" && safeAvatarBaseName(old) {
		if dir, err := h.avatarDir(); err == nil {
			_ = os.Remove(filepath.Join(dir, old))
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleAvatarServe は名前でアバターファイルを配信する。意図的に公開しており、
// <img> タグがロードできるようにする。名前は推測不能なランダム接尾辞だ。
// handleAvatarServe serves an avatar file by name. It is intentionally public
// so an <img> tag can load it; names are unguessable random suffixes.
func (h *Handler) handleAvatarServe(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// パス区切りと素のドット名を拒否する。すべての ".." 部分文字列を拒否しては
	// ならない。正当なアバター名は "<user>-<hex>.<ext>" で、ユーザー名の文字集合は
	// 連続ドットを許すため、"a..b-1a2b.png" は配信すべき実ファイルだ。下の
	// filepath.Base が既にトラバーサルをブロックする。
	// Reject path separators and bare dot names. Do NOT reject every ".."
	// substring: a legitimate avatar name is "<user>-<hex>.<ext>" and the
	// username charset allows consecutive dots, so "a..b-1a2b.png" is a real
	// file that must be served. filepath.Base below already blocks traversal.
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		http.NotFound(w, r)
		return
	}
	dir, err := h.avatarDir()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// logs.go と plugins.go がファイルを配信するのと同じように、シンボリックリンクを
	// 解決し包含を再確認する。http.ServeFile はシンボリックリンクを辿るため、
	// 書き込み可能なアバターディレクトリに仕込まれたシンボリックリンク
	// （evil.png -> /etc/passwd）は対象の内容を任意のクライアントへ漏らす。
	// このエンドポイントは意図的に公開で、<img> タグがロードできる。ディレクトリは
	// 今日 0700 で作成されるが、確認は設定方法に関係なく読み取り経路に属する。
	// 通常ファイルでないものも拒否し、デバイスやディレクトリを配信できないようにする。
	// Resolve symlinks and re-check containment, the same way logs.go and
	// plugins.go serve files. http.ServeFile follows symlinks, so a symlink
	// planted in a writable avatar directory (evil.png -> /etc/passwd) would
	// leak the target's contents to any client, and this endpoint is
	// intentionally public so <img> tags can load it. The directory is
	// created 0700 today, but the check belongs on the read path regardless
	// of how it was configured. Reject non-regular files as well, so devices
	// and directories cannot be served.
	realDir, derr := filepath.EvalSymlinks(dir)
	if derr != nil {
		http.NotFound(w, r)
		return
	}
	realPath, perr := filepath.EvalSymlinks(filepath.Join(realDir, filepath.Base(name)))
	if perr != nil {
		http.NotFound(w, r)
		return
	}
	if realPath != realDir && !strings.HasPrefix(realPath, realDir+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	info, ierr := os.Stat(realPath)
	if ierr != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, realPath)
}

// safeAvatarBaseName は filename がアバターディレクトリと filepath.Join して安全な
// 素のベース名かを報告する。非空、区切りなし、"."/".." でないこと。users.json を
// 改ざんできる攻撃者（または破損ファイル）が削除経路に任意ファイルを消させられては
// ならない。
// safeAvatarBaseName reports whether filename is a bare base name that is
// safe to filepath.Join with the avatar directory: non-empty, no separators,
// not "."/"..". An attacker who can tamper users.json (or a corrupted file)
// must not be able to make the delete path remove an arbitrary file.
func safeAvatarBaseName(filename string) bool {
	if filename == "" || filename == "." || filename == ".." {
		return false
	}
	return filename == filepath.Base(filename) && !strings.ContainsAny(filename, "/\\")
}
