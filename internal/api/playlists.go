package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

type playlistVideo struct {
	ID                string `json:"id"`
	URL               string `json:"url"`
	Title             string `json:"title"`
	Thumbnail         string `json:"thumbnail"`
	Duration          int64  `json:"duration"`
	Position          int    `json:"position"`
	ChannelName       string `json:"channelName"`
	ChannelURL        string `json:"channelUrl"`
	ChannelAvatar     string `json:"channelAvatar"`
	ViewCount         int64  `json:"viewCount"`
	AddedAt           int64  `json:"addedAt"`
	PublishedAt       int64  `json:"publishedAt"`
	WatchPosition     int64  `json:"watchPosition"`
	Watched           bool   `json:"watched"`
	ProgressUpdatedAt int64  `json:"progressUpdatedAt"`
}

type playlist struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Videos      []playlistVideo `json:"videos"`
	VideoCount  int             `json:"videoCount"`
	CreatedAt   int64           `json:"createdAt"`
}

const playlistVideoCols = `v.id, v.url, v.title, v.thumbnail, v.duration, v.position, v.channel_name, v.channel_url, v.channel_avatar,
v.view_count, v.added_at, v.published_at, COALESCE(p.position, 0), COALESCE(p.updated_at, 0)`

func scanPlaylistVideo(row interface{ Scan(...any) error }) (playlistVideo, error) {
	var v playlistVideo
	err := row.Scan(&v.ID, &v.URL, &v.Title, &v.Thumbnail, &v.Duration, &v.Position, &v.ChannelName, &v.ChannelURL, &v.ChannelAvatar,
		&v.ViewCount, &v.AddedAt, &v.PublishedAt, &v.WatchPosition, &v.ProgressUpdatedAt)
	v.Watched = v.Duration > 0 && v.WatchPosition*10 >= v.Duration*9
	return v, err
}

func (s *Server) playlistRoutes(rt *Router) {
	rt.Handle("GET", "/playlists", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		out, err := s.playlists(r.Context(), user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("POST", "/playlists", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		p, ok := readPlaylist(w, r)
		if !ok {
			return
		}
		p.ID, p.CreatedAt, p.Videos, p.VideoCount = auth.UUID(), time.Now().UnixMilli(), []playlistVideo{}, 0
		if _, err := s.DB.Exec(r.Context(), "INSERT INTO playlists (id, user_id, name, description, created_at) VALUES ($1, $2, $3, $4, $5)",
			p.ID, user, p.Name, p.Description, p.CreatedAt); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, p)
	}))
	rt.Handle("GET", "/playlists/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		p := playlist{ID: r.PathValue("id"), Videos: []playlistVideo{}}
		err := s.DB.QueryRow(r.Context(), "SELECT name, description, created_at FROM playlists WHERE id = $1 AND user_id = $2", p.ID, user).
			Scan(&p.Name, &p.Description, &p.CreatedAt)
		if isNoRows(err) {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		rows, err := s.DB.Query(r.Context(), "SELECT "+playlistVideoCols+` FROM playlist_videos v
LEFT JOIN progress p ON p.user_id = v.user_id AND p.video_url = v.url WHERE v.playlist_id = $1 AND v.user_id = $2 ORDER BY v.position`, p.ID, user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for rows.Next() {
			if v, err := scanPlaylistVideo(rows); err == nil {
				p.Videos = append(p.Videos, v)
			}
		}
		rows.Close()
		p.VideoCount = len(p.Videos)
		httpx.JSON(w, http.StatusOK, p)
	}))
	rt.Handle("PUT", "/playlists/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		p, ok := readPlaylist(w, r)
		if !ok {
			return
		}
		tag, err := s.DB.Exec(r.Context(), "UPDATE playlists SET name = $3, description = $4 WHERE id = $1 AND user_id = $2", r.PathValue("id"), user, p.Name, p.Description)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("DELETE", "/playlists/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		s.deleteRow(w, r, "DELETE FROM playlists WHERE id = $1 AND user_id = $2", r.PathValue("id"), user)
	}))
	rt.Handle("POST", "/playlists/{id}/videos", s.authed(s.playlistAdd))
	rt.Handle("PUT", "/playlists/{id}/reorder", s.authed(s.playlistReorder))
	rt.Handle("DELETE", "/playlists/{id}/videos/{videoUrl...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		s.deleteRow(w, r, "DELETE FROM playlist_videos WHERE playlist_id = $1 AND url = $2 AND user_id = $3", r.PathValue("id"), urlTail(r, "videoUrl"), user)
	}))
}

func readPlaylist(w http.ResponseWriter, r *http.Request) (playlist, bool) {
	var req struct {
		Name        *string `json:"name"`
		Description string  `json:"description"`
	}
	if !decodeOr(w, r, &req, "Invalid request body") {
		return playlist{}, false
	}
	if req.Name == nil {
		plain(w, r, http.StatusBadRequest, "Invalid request body")
		return playlist{}, false
	}
	return playlist{Name: *req.Name, Description: req.Description}, true
}

// playlists lists the user's playlists, newest first, each with its count and its first video only.
func (s *Server) playlists(ctx context.Context, user string) ([]playlist, error) {
	rows, err := s.DB.Query(ctx, `SELECT pl.id, pl.name, pl.description, pl.created_at,
(SELECT count(*) FROM playlist_videos c WHERE c.playlist_id = pl.id AND c.user_id = $1)
FROM playlists pl WHERE pl.user_id = $1 ORDER BY pl.created_at DESC`, user)
	if err != nil {
		return nil, err
	}
	out := []playlist{}
	for rows.Next() {
		p := playlist{Videos: []playlistVideo{}}
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt, &p.VideoCount); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	firsts, err := s.DB.Query(ctx, `SELECT DISTINCT ON (v.playlist_id) v.playlist_id, `+playlistVideoCols+` FROM playlist_videos v
LEFT JOIN progress p ON FALSE WHERE v.user_id = $1 ORDER BY v.playlist_id, v.position`, user)
	if err != nil {
		return nil, err
	}
	first := map[string]playlistVideo{}
	for firsts.Next() {
		var pid string
		var v playlistVideo
		err := firsts.Scan(&pid, &v.ID, &v.URL, &v.Title, &v.Thumbnail, &v.Duration, &v.Position, &v.ChannelName, &v.ChannelURL,
			&v.ChannelAvatar, &v.ViewCount, &v.AddedAt, &v.PublishedAt, &v.WatchPosition, &v.ProgressUpdatedAt)
		if err == nil {
			first[pid] = v
		}
	}
	firsts.Close()
	for i := range out {
		if v, ok := first[out[i].ID]; ok {
			out[i].Videos = []playlistVideo{v}
		}
	}
	return out, nil
}

func (s *Server) playlistAdd(w http.ResponseWriter, r *http.Request, user string) {
	var raw map[string]json.RawMessage
	if !decodeOr(w, r, &raw, "Invalid request body") {
		return
	}
	alias := map[string]string{"thumbnailUrl": "thumbnail", "uploaderName": "channelName", "uploaderUrl": "channelUrl",
		"channelAvatarUrl": "channelAvatar", "uploaderAvatarUrl": "channelAvatar"}
	for from, to := range alias {
		if v, ok := raw[from]; ok {
			if _, has := raw[to]; !has {
				raw[to] = v
			}
		}
	}
	for _, k := range []string{"url", "title", "thumbnail", "duration"} {
		if _, ok := raw[k]; !ok {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
	}
	b, _ := json.Marshal(raw)
	v := playlistVideo{PublishedAt: -1}
	if json.Unmarshal(b, &v) != nil {
		plain(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	pid := r.PathValue("id")
	if v.AddedAt <= 0 {
		v.AddedAt = time.Now().UnixMilli()
	}
	v.ID = auth.UUID()
	if err := s.DB.QueryRow(r.Context(), "SELECT count(*) FROM playlist_videos WHERE playlist_id = $1 AND user_id = $2", pid, user).Scan(&v.Position); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO playlist_videos (id, playlist_id, user_id, url, title, thumbnail, duration, position,
channel_name, channel_url, channel_avatar, view_count, added_at, published_at, metadata_refreshed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`, v.ID, pid, user, v.URL, v.Title, v.Thumbnail, v.Duration,
		v.Position, v.ChannelName, v.ChannelURL, v.ChannelAvatar, v.ViewCount, v.AddedAt, v.PublishedAt, time.Now().UnixMilli()); err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

func (s *Server) playlistReorder(w http.ResponseWriter, r *http.Request, user string) {
	var req struct {
		Order []string `json:"order"`
	}
	if !decodeOr(w, r, &req, "Invalid request body") {
		return
	}
	if req.Order == nil {
		plain(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	ctx, pid := r.Context(), r.PathValue("id")
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	var exists bool
	tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM playlists WHERE id = $1 AND user_id = $2)", pid, user).Scan(&exists)
	if !exists {
		plain(w, r, http.StatusNotFound, "Not found")
		return
	}
	rows, err := tx.Query(ctx, "SELECT url FROM playlist_videos WHERE playlist_id = $1 AND user_id = $2", pid, user)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	current := map[string]bool{}
	for rows.Next() {
		var u string
		rows.Scan(&u)
		current[u] = true
	}
	rows.Close()
	seen := map[string]bool{}
	for _, u := range req.Order {
		if seen[u] {
			plain(w, r, http.StatusBadRequest, "Duplicate video URLs")
			return
		}
		seen[u] = true
	}
	same := len(seen) == len(current)
	for u := range seen {
		same = same && current[u]
	}
	if !same {
		plain(w, r, http.StatusBadRequest, "Order must include every playlist video URL exactly once")
		return
	}
	for i, u := range req.Order {
		if _, err := tx.Exec(ctx, "UPDATE playlist_videos SET position = $4 WHERE playlist_id = $1 AND user_id = $2 AND url = $3", pid, user, u, i); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
