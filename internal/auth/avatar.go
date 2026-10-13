package auth

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxAvatarBytes bounds an avatar image upload.
const maxAvatarBytes = 1 << 20 // 1 MiB

// allowedAvatarExt maps a sniffed content type to the file extension we keep.
var allowedAvatarExt = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

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

// avatarFileName returns a safe, unique file name for a user's avatar.
func avatarFileName(username, ext string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return sanitizeUser(username) + "-" + hex.EncodeToString(b) + ext
}

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
	if err := os.WriteFile(path, data, 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, "保存に失敗しました")
		return
	}

	old := h.store.AvatarOf(sess.Username)
	if err := h.store.SetAvatar(sess.Username, name); err != nil {
		_ = os.Remove(path)
		writeError(w, http.StatusInternalServerError, "保存に失敗しました")
		return
	}
	// Remove the previous avatar file, if any.
	if old != "" && old != name {
		_ = os.Remove(filepath.Join(dir, old))
	}

	if h.logger != nil {
		h.logger.Info("アバター更新: user=%s", sess.Username)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "avatar": name})
}

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
	if old != "" {
		if dir, err := h.avatarDir(); err == nil {
			_ = os.Remove(filepath.Join(dir, old))
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleAvatarServe serves an avatar file by name. It is intentionally public
// so an <img> tag can load it; names are unguessable random suffixes.
func (h *Handler) handleAvatarServe(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	dir, err := h.avatarDir()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(dir, filepath.Base(name))
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, path)
}
