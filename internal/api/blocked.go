package api

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

type blockedItem struct {
	URL          string  `json:"url"`
	Name         *string `json:"name"`
	ThumbnailURL *string `json:"thumbnailUrl"`
	BlockedAt    int64   `json:"blockedAt"`
	Global       *bool   `json:"global"`
}

type blockedKeyword struct {
	Keyword   string `json:"keyword"`
	BlockedAt int64  `json:"blockedAt"`
	Global    *bool  `json:"global"`
}

func normalizeKeyword(v string) string {
	return strings.ToLower(strings.TrimSpace(norm.NFKC.String(v)))
}

var schemeSlash = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*):/([^/])`)

// urlTail is urlTailParameter: the tail path value, with the "https:/" a proxy may have folded back to "https://",
// and the query string of the request when the value has none (a watch URL's "?v=" ends up there).
func urlTail(r *http.Request, name string) string {
	v := schemeSlash.ReplaceAllString(r.PathValue(name), "$1://$2")
	if q := r.URL.RawQuery; q != "" && !strings.Contains(v, "?") {
		return v + "?" + q
	}
	return v
}

func boolp(b bool) *bool { return &b }

func (s *Server) roleOf(r *http.Request, user string) string {
	role, err := s.Auth.Role(r.Context(), user)
	if err != nil || role == nil {
		return "user"
	}
	return *role
}

func canModerate(role string) bool { return role == "admin" || role == "moderator" }

func (s *Server) blockedRoutes(rt *Router) {
	for _, kind := range []string{"channels", "videos"} {
		kind := kind
		rt.Handle("GET", "/blocked/"+kind, s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
			items, err := s.blockedList(r, user, kind)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, items)
		}))
		rt.Handle("POST", "/blocked/"+kind, s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
			var req struct {
				URL          *string `json:"url"`
				Name         *string `json:"name"`
				ThumbnailURL *string `json:"thumbnailUrl"`
				Global       *bool   `json:"global"`
			}
			if !decodeOr(w, r, &req, "Invalid request body") {
				return
			}
			if req.URL == nil {
				plain(w, r, http.StatusBadRequest, "Invalid request body")
				return
			}
			scope := "user"
			if req.Global != nil && *req.Global && canModerate(s.roleOf(r, user)) {
				scope = "global"
			}
			now := time.Now().UnixMilli()
			var err error
			out := blockedItem{URL: *req.URL, BlockedAt: now}
			if kind == "channels" {
				out.Name, out.ThumbnailURL = req.Name, req.ThumbnailURL
				_, err = s.DB.Exec(r.Context(), `INSERT INTO blocked_channels (user_id, scope, channel_url, name, thumbnail_url, blocked_at)
VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (user_id, channel_url) DO UPDATE SET scope = EXCLUDED.scope, name = EXCLUDED.name,
thumbnail_url = EXCLUDED.thumbnail_url, blocked_at = EXCLUDED.blocked_at`, user, scope, *req.URL, req.Name, req.ThumbnailURL, now)
			} else {
				_, err = s.DB.Exec(r.Context(), `INSERT INTO blocked_videos (user_id, scope, video_url, blocked_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, video_url) DO UPDATE SET scope = EXCLUDED.scope, blocked_at = EXCLUDED.blocked_at`, user, scope, *req.URL, now)
			}
			if err != nil {
				s.fail(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusCreated, out)
		}))
		param := map[string]string{"channels": "channelUrl", "videos": "videoUrl"}[kind]
		rt.Handle("DELETE", "/blocked/"+kind+"/{"+param+"...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
			target := urlTail(r, param)
			if target == "" {
				plain(w, r, http.StatusBadRequest, "Missing "+param)
				return
			}
			table, col := "blocked_channels", "channel_url"
			if kind == "videos" {
				table, col = "blocked_videos", "video_url"
			}
			s.deleteBlocked(w, r, user, table, col, target)
		}))
	}
	rt.Handle("GET", "/blocked/keywords", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		rows, err := s.DB.Query(r.Context(), `SELECT keyword, blocked_at, scope FROM blocked_keywords
WHERE user_id = $1 OR scope = 'global' ORDER BY blocked_at DESC`, user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		defer rows.Close()
		out := []blockedKeyword{}
		for rows.Next() {
			var k blockedKeyword
			var scope string
			if err := rows.Scan(&k.Keyword, &k.BlockedAt, &scope); err != nil {
				s.fail(w, r, err)
				return
			}
			k.Global = boolp(scope == "global")
			out = append(out, k)
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("POST", "/blocked/keywords", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			Keyword *string `json:"keyword"`
			Global  *bool   `json:"global"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.Keyword == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		kw := strings.TrimSpace(*req.Keyword)
		if kw == "" || auth.JavaLength(kw) > 100 {
			plain(w, r, http.StatusBadRequest, "Keyword must contain 1 to 100 characters")
			return
		}
		global := req.Global != nil && *req.Global && canModerate(s.roleOf(r, user))
		scope := "user"
		if global {
			scope = "global"
		}
		n := normalizeKeyword(kw)
		if n == "" {
			plain(w, r, http.StatusBadRequest, "Keyword cannot be empty")
			return
		}
		now := time.Now().UnixMilli()
		_, err := s.DB.Exec(r.Context(), `INSERT INTO blocked_keywords (user_id, scope, keyword, blocked_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, keyword) DO UPDATE SET scope = EXCLUDED.scope, blocked_at = EXCLUDED.blocked_at`, user, scope, n, now)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, blockedKeyword{Keyword: n, BlockedAt: now, Global: boolp(global)})
	}))
	rt.Handle("DELETE", "/blocked/keywords/{keyword...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		kw := r.PathValue("keyword")
		if kw == "" {
			plain(w, r, http.StatusBadRequest, "Missing keyword")
			return
		}
		s.deleteBlocked(w, r, user, "blocked_keywords", "keyword", normalizeKeyword(kw))
	}))
}

func (s *Server) blockedList(r *http.Request, user, kind string) ([]blockedItem, error) {
	q := `SELECT channel_url, name, thumbnail_url, blocked_at, scope FROM blocked_channels WHERE user_id = $1 OR scope = 'global' ORDER BY blocked_at DESC`
	if kind == "videos" {
		q = `SELECT video_url, NULL::text, NULL::text, blocked_at, scope FROM blocked_videos WHERE user_id = $1 OR scope = 'global' ORDER BY blocked_at DESC`
	}
	rows, err := s.DB.Query(r.Context(), q, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []blockedItem{}
	for rows.Next() {
		var it blockedItem
		var scope string
		if err := rows.Scan(&it.URL, &it.Name, &it.ThumbnailURL, &it.BlockedAt, &scope); err != nil {
			return nil, err
		}
		it.Global = boolp(scope == "global")
		out = append(out, it)
	}
	return out, rows.Err()
}

// deleteBlocked removes the caller's own entry; moderators and admins may also remove a global one.
func (s *Server) deleteBlocked(w http.ResponseWriter, r *http.Request, user, table, col, value string) {
	scope := "scope = 'user' AND user_id = $2"
	if canModerate(s.roleOf(r, user)) {
		scope = "(scope = 'global' OR user_id = $2)"
	}
	tag, err := s.DB.Exec(r.Context(), "DELETE FROM "+table+" WHERE "+col+" = $1 AND "+scope, value, user)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		plain(w, r, http.StatusNotFound, "Not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
