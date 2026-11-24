package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/store"
	"typetype.dev/server/internal/youtube"
)

type allowedChannel struct {
	URL          string  `json:"url"`
	Name         *string `json:"name"`
	ThumbnailURL *string `json:"thumbnailUrl"`
	AllowedAt    int64   `json:"allowedAt"`
	Global       *bool   `json:"global"`
}

type allowedPlaylist struct {
	URL          string  `json:"url"`
	Title        *string `json:"title"`
	ThumbnailURL *string `json:"thumbnailUrl"`
	UploaderName *string `json:"uploaderName"`
	AllowedAt    int64   `json:"allowedAt"`
	Global       *bool   `json:"global"`
}

func playlistKey(v string) string {
	v, _, _ = strings.Cut(strings.TrimSpace(v), "#")
	return strings.ReplaceAll(strings.TrimSuffix(v, "/"), "http://", "https://")
}

// where is "user" (the user's own rows), "global", or "both".
func (s *Server) allowedChannels(ctx context.Context, user, where string) []allowedChannel {
	q := map[string]string{"user": "user_id = $1 AND scope = 'user'", "global": "scope = 'global'", "both": "user_id = $1 OR scope = 'global'"}[where]
	args := []any{}
	if where != "global" {
		args = append(args, user)
	}
	out := []allowedChannel{}
	rows, err := s.DB.Query(ctx, "SELECT channel_url, name, thumbnail_url, allowed_at, scope FROM allowed_channels WHERE "+q+" ORDER BY allowed_at DESC", args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c allowedChannel
		var scope string
		if rows.Scan(&c.URL, &c.Name, &c.ThumbnailURL, &c.AllowedAt, &scope) == nil {
			c.Global = boolp(scope == "global")
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) allowedPlaylists(ctx context.Context, user, where string) []allowedPlaylist {
	q := map[string]string{"user": "user_id = $1 AND scope = 'user'", "global": "scope = 'global'", "both": "user_id = $1 OR scope = 'global'"}[where]
	args := []any{}
	if where != "global" {
		args = append(args, user)
	}
	out := []allowedPlaylist{}
	rows, err := s.DB.Query(ctx, "SELECT playlist_url, title, thumbnail_url, uploader_name, allowed_at, scope FROM allowed_playlists WHERE "+q+
		" ORDER BY allowed_at DESC", args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p allowedPlaylist
		var scope string
		if rows.Scan(&p.URL, &p.Title, &p.ThumbnailURL, &p.UploaderName, &p.AllowedAt, &scope) == nil {
			p.Global = boolp(scope == "global")
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) addAllowedChannel(ctx context.Context, user string, c allowedChannel, global bool) (allowedChannel, error) {
	c.URL, c.AllowedAt, c.Global = channelKey(c.URL), time.Now().UnixMilli(), &global
	scope, clause := "user", "scope = 'user' AND user_id = $2"
	if global {
		scope, clause = "global", "scope = 'global' AND $2 = $2"
	}
	if _, err := s.DB.Exec(ctx, "DELETE FROM allowed_channels WHERE channel_url = $1 AND "+clause, c.URL, user); err != nil {
		return c, err
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO allowed_channels (user_id, scope, channel_url, name, thumbnail_url, allowed_at) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, channel_url) DO UPDATE SET scope = EXCLUDED.scope, name = EXCLUDED.name, thumbnail_url = EXCLUDED.thumbnail_url,
allowed_at = EXCLUDED.allowed_at`, user, scope, c.URL, c.Name, c.ThumbnailURL, c.AllowedAt)
	return c, err
}

func (s *Server) addAllowedPlaylist(ctx context.Context, user string, p allowedPlaylist, global bool) (allowedPlaylist, error) {
	p.URL, p.AllowedAt, p.Global = playlistKey(p.URL), time.Now().UnixMilli(), &global
	scope, clause := "user", "scope = 'user' AND user_id = $2"
	if global {
		scope, clause = "global", "scope = 'global' AND $2 = $2"
	}
	if _, err := s.DB.Exec(ctx, "DELETE FROM allowed_playlists WHERE playlist_url = $1 AND "+clause, p.URL, user); err != nil {
		return p, err
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO allowed_playlists (user_id, scope, playlist_url, title, thumbnail_url, uploader_name, allowed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`, user, scope, p.URL, p.Title, p.ThumbnailURL, p.UploaderName, p.AllowedAt)
	return p, err
}

// accessProfile is AccessControlService.profileFor: nil means unrestricted.
type accessProfile struct {
	channels  []allowedChannel
	playlists []allowedPlaylist
}

func (s *Server) accessFor(ctx context.Context, user string) *accessProfile {
	if user != "" {
		if role, err := s.Auth.Role(ctx, user); err == nil && role != nil && *role == "admin" {
			return nil
		}
	}
	st, err := s.Admin.Get(ctx)
	global := err == nil && st.AccessMode == "allow_list"
	list := func(u string) *accessProfile {
		if u == "" {
			return &accessProfile{s.allowedChannels(ctx, "", "global"), s.allowedPlaylists(ctx, "", "global")}
		}
		return &accessProfile{s.allowedChannels(ctx, u, "both"), s.allowedPlaylists(ctx, u, "both")}
	}
	if user == "" || strings.HasPrefix(user, "guest:") {
		if global {
			return list("")
		}
		return nil
	}
	var mode string
	var managed bool
	s.DB.QueryRow(ctx, "SELECT access_mode, access_mode_admin_managed FROM settings WHERE user_id = $1", user).Scan(&mode, &managed)
	mode = store.ToAccessMode(mode)
	if global {
		if managed && mode == "unrestricted" {
			return nil
		}
		return list(user)
	}
	if mode == "allow_list" {
		return list(user)
	}
	return nil
}

func (p *accessProfile) allowsChannel(u, name string) bool {
	if p == nil {
		return true
	}
	key, n := channelKey(u), strings.TrimSpace(name)
	for _, c := range p.channels {
		if channelKey(c.URL) == key || c.Name != nil && strings.TrimSpace(*c.Name) != "" && n != "" && strings.EqualFold(strings.TrimSpace(*c.Name), n) {
			return true
		}
	}
	return false
}

func (p *accessProfile) allowsUploader(u, name string) bool {
	if p == nil {
		return true
	}
	return (u != "" || name != "") && p.allowsChannel(u, name)
}

func (p *accessProfile) filter(vs []youtube.VideoItem) []youtube.VideoItem {
	if p == nil {
		return vs
	}
	out := vs[:0:0]
	for _, v := range vs {
		if p.allowsUploader(v.UploaderURL, v.UploaderName) {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) allowedRoutes(rt *Router) {
	rt.Handle("GET", "/allowed/channels", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		httpx.JSON(w, http.StatusOK, s.allowedChannels(r.Context(), user, "both"))
	}))
	rt.Handle("POST", "/allowed/channels", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var c allowedChannel
		if !decodeOr(w, r, &c, "Invalid request body") || c.URL == "" {
			if c.URL == "" {
				plain(w, r, http.StatusBadRequest, "Invalid request body")
			}
			return
		}
		role, _ := s.Auth.Role(r.Context(), user)
		global := role != nil && (*role == "admin" || *role == "moderator") && c.Global != nil && *c.Global
		saved, err := s.addAllowedChannel(r.Context(), user, c, global)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, saved)
	}))
	rt.Handle("DELETE", "/allowed/channels/{channelUrl...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		u := urlTail(r, "channelUrl")
		role, _ := s.Auth.Role(r.Context(), user)
		clause := "scope = 'user' AND user_id = $2"
		if role != nil && (*role == "admin" || *role == "moderator") {
			clause = "(scope = 'global' OR user_id = $2)"
		}
		tag, err := s.DB.Exec(r.Context(), "DELETE FROM allowed_channels WHERE channel_url = $1 AND "+clause, channelKey(u), user)
		if err != nil || tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("GET", "/admin/allowed/playlists", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		httpx.JSON(w, http.StatusOK, s.allowedPlaylists(r.Context(), "", "global"))
	}))
	rt.Handle("POST", "/admin/allowed/playlists", s.adminOnly(func(w http.ResponseWriter, r *http.Request, admin string) {
		var p allowedPlaylist
		if !decodeOr(w, r, &p, "Invalid request body") {
			return
		}
		saved, err := s.addAllowedPlaylist(r.Context(), admin, p, true)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, saved)
	}))
	rt.Handle("DELETE", "/admin/allowed/playlists/{playlistUrl...}", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		tag, err := s.DB.Exec(r.Context(), "DELETE FROM allowed_playlists WHERE playlist_url = $1 AND scope = 'global'", playlistKey(urlTail(r, "playlistUrl")))
		if err != nil || tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	s.adminAllowRoutes(rt)
}

func (s *Server) adminAllowRoutes(rt *Router) {
	target := func(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
		users, err := s.adminUsers(r.Context(), "WHERE u.id = $1", r.PathValue("id"))
		if err != nil || len(users) == 0 {
			plain(w, r, http.StatusNotFound, "User not found")
			return nil, false
		}
		u := users[0]
		return map[string]string{"id": u.ID, "email": u.Email, "name": u.Name, "accessMode": u.AccessMode}, true
	}
	rt.Handle("GET", "/admin/users/{id}/allow-list", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		u, ok := target(w, r)
		if !ok {
			return
		}
		ctx, id := r.Context(), r.PathValue("id")
		httpx.JSON(w, http.StatusOK, map[string]any{"user": u, "globalChannels": s.allowedChannels(ctx, "", "global"),
			"userChannels": s.allowedChannels(ctx, id, "user"), "globalPlaylists": s.allowedPlaylists(ctx, "", "global"),
			"userPlaylists": s.allowedPlaylists(ctx, id, "user")})
	}))
	rt.Handle("POST", "/admin/users/{id}/allowed/channels", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		if _, ok := target(w, r); !ok {
			return
		}
		var c allowedChannel
		if !decodeOr(w, r, &c, "Invalid request body") {
			return
		}
		saved, err := s.addAllowedChannel(r.Context(), r.PathValue("id"), c, false)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, saved)
	}))
	rt.Handle("DELETE", "/admin/users/{id}/allowed/channels/{channelUrl...}", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		if _, ok := target(w, r); !ok {
			return
		}
		tag, err := s.DB.Exec(r.Context(), "DELETE FROM allowed_channels WHERE channel_url = $1 AND scope = 'user' AND user_id = $2",
			channelKey(urlTail(r, "channelUrl")), r.PathValue("id"))
		if err != nil || tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("POST", "/admin/users/{id}/allowed/playlists", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		if _, ok := target(w, r); !ok {
			return
		}
		var p allowedPlaylist
		if !decodeOr(w, r, &p, "Invalid request body") {
			return
		}
		saved, err := s.addAllowedPlaylist(r.Context(), r.PathValue("id"), p, false)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, saved)
	}))
	rt.Handle("DELETE", "/admin/users/{id}/allowed/playlists/{playlistUrl...}", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		if _, ok := target(w, r); !ok {
			return
		}
		tag, err := s.DB.Exec(r.Context(), "DELETE FROM allowed_playlists WHERE playlist_url = $1 AND scope = 'user' AND user_id = $2",
			playlistKey(urlTail(r, "playlistUrl")), r.PathValue("id"))
		if err != nil || tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("GET", "/admin/users/managed-access", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		q := r.URL.Query()
		limit, offset := 50, 0
		if q.Has("limit") {
			n, err := strconv.Atoi(q.Get("limit"))
			if err != nil || n < 1 || n > 200 {
				plain(w, r, http.StatusBadRequest, "Invalid limit")
				return
			}
			limit = n
		}
		if q.Has("page") {
			n, err := strconv.Atoi(q.Get("page"))
			if err != nil || n < 0 {
				plain(w, r, http.StatusBadRequest, "Invalid page")
				return
			}
			offset = n
		}
		rows, err := s.DB.Query(r.Context(), `SELECT u.id, u.email, u.name, s.access_mode FROM settings s JOIN users u ON u.id = s.user_id
WHERE s.access_mode_admin_managed = true ORDER BY s.access_mode_admin_managed_at DESC, lower(u.email), lower(u.name), u.id LIMIT $1 OFFSET $2`, limit+1, offset)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		items := []map[string]any{}
		for rows.Next() {
			var id, email, name, mode string
			if rows.Scan(&id, &email, &name, &mode) == nil {
				items = append(items, map[string]any{"id": id, "email": email, "name": name, "accessMode": store.ToAccessMode(mode), "adminManagedAccessMode": true})
			}
		}
		rows.Close()
		var next *string
		if len(items) > limit {
			items = items[:limit]
			n := strconv.Itoa(offset + limit)
			next = &n
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextpage": next})
	}))
}
