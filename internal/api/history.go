package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/store"
)

type historyItem struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	Thumbnail     string `json:"thumbnail"`
	ChannelName   string `json:"channelName"`
	ChannelURL    string `json:"channelUrl"`
	ChannelAvatar string `json:"channelAvatar"`
	Duration      int64  `json:"duration"`
	Progress      int64  `json:"progress"`
	WatchedAt     int64  `json:"watchedAt"`
}

func (s *Server) historyRoutes(rt *Router) {
	rt.Handle("GET", "/history", s.authed(s.historyList))
	rt.Handle("POST", "/history", s.authed(s.historyAdd))
	rt.Handle("DELETE", "/history/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var url string
		err := s.DB.QueryRow(r.Context(), "DELETE FROM history WHERE id = $1 AND user_id = $2 RETURNING url", r.PathValue("id"), user).Scan(&url)
		if isNoRows(err) {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.DB.Exec(r.Context(), "DELETE FROM progress WHERE user_id = $1 AND video_url = $2", user, url)
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("DELETE", "/history", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if _, err := s.DB.Exec(r.Context(), "DELETE FROM history WHERE user_id = $1", user); err != nil {
			s.fail(w, r, err)
			return
		}
		s.DB.Exec(r.Context(), "DELETE FROM progress WHERE user_id = $1", user)
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("GET", "/search-history", s.authed(s.searchHistoryList))
	rt.Handle("POST", "/search-history", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			Term *string `json:"term"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.Term == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		id, now := auth.UUID(), time.Now().UnixMilli()
		if _, err := s.DB.Exec(r.Context(), "INSERT INTO search_history (id, user_id, term, searched_at) VALUES ($1, $2, $3, $4)", id, user, *req.Term, now); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "term": *req.Term, "searchedAt": now})
	}))
	rt.Handle("DELETE", "/search-history/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		tag, err := s.DB.Exec(r.Context(), "DELETE FROM search_history WHERE id = $1 AND user_id = $2", r.PathValue("id"), user)
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
	rt.Handle("DELETE", "/search-history", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if _, err := s.DB.Exec(r.Context(), "DELETE FROM search_history WHERE user_id = $1", user); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

func queryLong(r *http.Request, key string) (*int64, bool) {
	v, ok := r.URL.Query()[key]
	if !ok {
		return nil, true
	}
	n, err := strconv.ParseInt(v[0], 10, 64)
	return &n, err == nil
}

func (s *Server) historyList(w http.ResponseWriter, r *http.Request, user string) {
	from, ok := queryLong(r, "from")
	if !ok {
		plain(w, r, http.StatusBadRequest, "Invalid from")
		return
	}
	to, ok := queryLong(r, "to")
	if !ok {
		plain(w, r, http.StatusBadRequest, "Invalid to")
		return
	}
	if from != nil && to != nil && *from >= *to {
		plain(w, r, http.StatusBadRequest, "from must be lower than to")
		return
	}
	limit := int64(60)
	if l, ok := queryLong(r, "limit"); !ok {
		plain(w, r, http.StatusBadRequest, "Invalid limit")
		return
	} else if l != nil {
		limit = max(1, min(*l, 1000))
	}
	offset := int64(0)
	if o, ok := queryLong(r, "offset"); !ok || o != nil && *o < 0 {
		plain(w, r, http.StatusBadRequest, "Invalid offset")
		return
	} else if o != nil {
		offset = *o
	}
	where, args := "user_id = $1", []any{user}
	if q := r.URL.Query().Get("q"); strings.TrimSpace(q) != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		where += " AND (lower(title) LIKE $2 OR lower(channel_name) LIKE $2)"
	}
	if from != nil {
		args = append(args, *from)
		where += " AND watched_at >= $" + strconv.Itoa(len(args))
	}
	if to != nil {
		args = append(args, *to)
		where += " AND watched_at < $" + strconv.Itoa(len(args))
	}
	ctx := r.Context()
	var total int64
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM history WHERE "+where, args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.DB.Query(ctx, `SELECT h.id, h.url, h.title, h.thumbnail, h.channel_name, h.channel_url, h.channel_avatar, h.duration,
GREATEST(h.progress, COALESCE(p.position, 0) / 1000), h.watched_at
FROM (SELECT * FROM history WHERE `+where+` ORDER BY watched_at DESC, id DESC LIMIT `+strconv.FormatInt(limit, 10)+` OFFSET `+strconv.FormatInt(offset, 10)+`) h
LEFT JOIN progress p ON p.user_id = h.user_id AND p.video_url = h.url ORDER BY h.watched_at DESC, h.id DESC`, args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items := []historyItem{}
	for rows.Next() {
		var it historyItem
		if err := rows.Scan(&it.ID, &it.URL, &it.Title, &it.Thumbnail, &it.ChannelName, &it.ChannelURL, &it.ChannelAvatar,
			&it.Duration, &it.Progress, &it.WatchedAt); err != nil {
			rows.Close()
			s.fail(w, r, err)
			return
		}
		it.Title, it.Thumbnail = store.FallbackTitle(it.Title, it.URL), store.FallbackThumbnail(it.Thumbnail, it.URL)
		items = append(items, it)
	}
	rows.Close()
	s.fillAvatars(ctx, user, items)
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	httpx.JSON(w, http.StatusOK, items)
}

// fillAvatars gives entries saved without a channel avatar the one of the matching subscription.
func (s *Server) fillAvatars(ctx context.Context, user string, items []historyItem) {
	var urls []string
	for _, it := range items {
		if strings.TrimSpace(it.ChannelAvatar) == "" && strings.TrimSpace(it.ChannelURL) != "" {
			urls = append(urls, db.CanonicalChannelURL(it.ChannelURL))
		}
	}
	if len(urls) == 0 {
		return
	}
	rows, err := s.DB.Query(ctx, "SELECT channel_url, avatar_url FROM subscriptions WHERE user_id = $1 AND channel_url = ANY($2) AND avatar_url <> ''", user, urls)
	if err != nil {
		return
	}
	avatars := map[string]string{}
	for rows.Next() {
		var u, a string
		if rows.Scan(&u, &a) == nil {
			avatars[db.CanonicalChannelURL(u)] = a
		}
	}
	rows.Close()
	for i := range items {
		if strings.TrimSpace(items[i].ChannelAvatar) == "" {
			if a, ok := avatars[db.CanonicalChannelURL(items[i].ChannelURL)]; ok {
				items[i].ChannelAvatar = a
			}
		}
	}
}

func (s *Server) historyAdd(w http.ResponseWriter, r *http.Request, user string) {
	var it historyItem
	var req struct {
		URL, Title, Thumbnail, ChannelName, ChannelURL *string
		ChannelAvatar                                  string `json:"channelAvatar"`
		Duration, Progress                             *int64
		ID                                             string `json:"id"`
		WatchedAt                                      int64  `json:"watchedAt"`
	}
	if !decodeOr(w, r, &req, "Invalid request body") {
		return
	}
	if req.URL == nil || req.Title == nil || req.Thumbnail == nil || req.ChannelName == nil || req.ChannelURL == nil || req.Duration == nil || req.Progress == nil {
		plain(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	it = historyItem{ID: req.ID, URL: *req.URL, Title: *req.Title, Thumbnail: *req.Thumbnail, ChannelName: *req.ChannelName,
		ChannelURL: *req.ChannelURL, ChannelAvatar: req.ChannelAvatar, Duration: *req.Duration, Progress: *req.Progress, WatchedAt: req.WatchedAt}
	st, err := store.GetSettings(r.Context(), s.DB, user)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if st.DisableWatchHistory {
		httpx.JSON(w, http.StatusCreated, it)
		return
	}
	it.Title, it.Thumbnail = store.FallbackTitle(it.Title, it.URL), store.FallbackThumbnail(it.Thumbnail, it.URL)
	var saved int64
	if err := s.DB.QueryRow(r.Context(), "SELECT COALESCE(max(position), 0) / 1000 FROM progress WHERE user_id = $1 AND video_url = $2", user, it.URL).Scan(&saved); err != nil {
		s.fail(w, r, err)
		return
	}
	it.Progress = max(it.Progress, saved)
	it.ID, it.WatchedAt = auth.UUID(), time.Now().UnixMilli()
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO history (id, user_id, url, title, thumbnail, channel_name, channel_url, channel_avatar,
duration, progress, watched_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, it.ID, user, it.URL, it.Title, it.Thumbnail,
		it.ChannelName, it.ChannelURL, it.ChannelAvatar, it.Duration, it.Progress, it.WatchedAt); err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, it)
}

func (s *Server) searchHistoryList(w http.ResponseWriter, r *http.Request, user string) {
	page, limit := 1, 20
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			page = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if page < 1 {
		plain(w, r, http.StatusBadRequest, "Invalid page")
		return
	}
	if limit < 1 || limit > 100 {
		plain(w, r, http.StatusBadRequest, "Invalid limit")
		return
	}
	var total int64
	if err := s.DB.QueryRow(r.Context(), "SELECT count(*) FROM search_history WHERE user_id = $1", user).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.DB.Query(r.Context(), "SELECT id, term, searched_at FROM search_history WHERE user_id = $1 ORDER BY searched_at DESC LIMIT $2 OFFSET $3",
		user, limit, int64(page-1)*int64(limit))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	type item struct {
		ID         string `json:"id"`
		Term       string `json:"term"`
		SearchedAt int64  `json:"searchedAt"`
	}
	out := []item{}
	for rows.Next() {
		var it item
		if rows.Scan(&it.ID, &it.Term, &it.SearchedAt) == nil {
			out = append(out, it)
		}
	}
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	httpx.JSON(w, http.StatusOK, out)
}
