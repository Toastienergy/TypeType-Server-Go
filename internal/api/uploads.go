package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
)

// imageType is UploadedImages.mediaType: the format read from the file's own signature, never from its name.
func imageType(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

func imageVersion(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:20]
}

// readUpload reads a raw request body of at most limit bytes; ok is false when it is larger.
func readUpload(r *http.Request, limit int64) ([]byte, bool) {
	if n, err := strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64); err == nil && n > limit {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	return b, err == nil && int64(len(b)) <= limit
}

func (s *Server) uploadRoutes(rt *Router) {
	rt.Handle("PUT", "/profile/avatar/custom", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if strings.HasPrefix(user, "guest:") {
			plain(w, r, http.StatusForbidden, "Guest users cannot change avatar")
			return
		}
		b, ok := readUpload(r, 10<<20)
		if !ok {
			plain(w, r, http.StatusRequestEntityTooLarge, "AVATAR_TOO_LARGE")
			return
		}
		mt := imageType(b)
		if mt == "" {
			plain(w, r, http.StatusUnsupportedMediaType, "AVATAR_FORMAT_UNSUPPORTED")
			return
		}
		v, now := imageVersion(b), time.Now().UnixMilli()
		u := "/avatar/custom/" + user + "/" + v
		tag, err := s.DB.Exec(r.Context(), "UPDATE users SET avatar_type = 'custom', avatar_code = NULL, avatar_url = $2, updated_at = $3 WHERE id = $1", user, u, now)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "User not found")
			return
		}
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO user_avatars (user_id, media_type, content, version, updated_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id) DO UPDATE SET media_type = EXCLUDED.media_type, content = EXCLUDED.content, version = EXCLUDED.version, updated_at = EXCLUDED.updated_at`,
			user, mt, b, v, now); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"avatarUrl": u, "mediaType": mt, "size": len(b)})
	}))
	rt.Handle("PUT", "/profile/seekbar-icon", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if strings.HasPrefix(user, "guest:") {
			plain(w, r, http.StatusForbidden, "Guest users cannot change the seekbar icon")
			return
		}
		b, ok := readUpload(r, 2<<20)
		if !ok {
			plain(w, r, http.StatusRequestEntityTooLarge, "SEEKBAR_ICON_TOO_LARGE")
			return
		}
		mt := imageType(b)
		if mt == "" {
			plain(w, r, http.StatusUnsupportedMediaType, "SEEKBAR_ICON_FORMAT_UNSUPPORTED")
			return
		}
		var exists bool
		s.DB.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", user).Scan(&exists)
		if !exists {
			plain(w, r, http.StatusNotFound, "User not found")
			return
		}
		v, now := imageVersion(b), time.Now().UnixMilli()
		u := "/seekbar-icon/" + user + "/" + v
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO user_seekbar_icons (user_id, media_type, content, version, updated_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id) DO UPDATE SET media_type = EXCLUDED.media_type, content = EXCLUDED.content, version = EXCLUDED.version, updated_at = EXCLUDED.updated_at`,
			user, mt, b, v, now); err != nil {
			s.fail(w, r, err)
			return
		}
		s.DB.Exec(r.Context(), `INSERT INTO settings (user_id, seekbar_icon, seekbar_icon_url) VALUES ($1, 'custom', $2)
ON CONFLICT (user_id) DO UPDATE SET seekbar_icon = 'custom', seekbar_icon_url = EXCLUDED.seekbar_icon_url`, user, u)
		httpx.JSON(w, http.StatusOK, map[string]any{"seekbarIconUrl": u, "mediaType": mt, "size": len(b)})
	}))
	rt.Handle("DELETE", "/profile/seekbar-icon", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		s.DB.Exec(r.Context(), "DELETE FROM user_seekbar_icons WHERE user_id = $1", user)
		s.DB.Exec(r.Context(), "UPDATE settings SET seekbar_icon = 'typetype' WHERE user_id = $1 AND seekbar_icon = 'custom'", user)
		s.DB.Exec(r.Context(), "UPDATE settings SET seekbar_icon_url = '' WHERE user_id = $1", user)
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("GET", "/seekbar-icon/{userId}/{version}", func(w http.ResponseWriter, r *http.Request) {
		var mt string
		var content []byte
		var v string
		err := s.DB.QueryRow(r.Context(), "SELECT media_type, content, version FROM user_seekbar_icons WHERE user_id = $1 AND version = $2",
			r.PathValue("userId"), r.PathValue("version")).Scan(&mt, &content, &v)
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, "Icon not found")
			return
		}
		etag := `"` + v + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", mt)
		w.Write(content)
	})
}
