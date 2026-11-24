package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/store"
)

type subscriptionItem struct {
	ChannelURL   string    `json:"channelUrl"`
	Name         string    `json:"name"`
	AvatarURL    string    `json:"avatarUrl"`
	SubscribedAt int64     `json:"subscribedAt"`
	GroupIDs     *[]string `json:"groupIds,omitempty"`
}

type progressItem struct {
	VideoURL  string `json:"videoUrl"`
	Position  int64  `json:"position"`
	UpdatedAt int64  `json:"updatedAt"`
}

type mediaItem struct {
	URL           string `json:"-"`
	Title         string `json:"title"`
	Thumbnail     string `json:"thumbnail"`
	Duration      int64  `json:"duration"`
	At            int64  `json:"-"`
	ChannelName   string `json:"channelName"`
	ChannelURL    string `json:"channelUrl"`
	ChannelAvatar string `json:"channelAvatar"`
	ViewCount     int64  `json:"viewCount"`
	PublishedAt   int64  `json:"publishedAt"`
}

func (s *Server) collectionRoutes(rt *Router) {
	rt.Handle("GET", "/subscriptions", s.authed(s.subscriptionList))
	rt.Handle("GET", "/subscriptions/group-memberships", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		items, err := s.subscriptions(r, user, "", false, true)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, items)
	}))
	rt.Handle("POST", "/subscriptions", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct{ ChannelURL, Name, AvatarURL *string }
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.ChannelURL == nil || req.Name == nil || req.AvatarURL == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		it := subscriptionItem{ChannelURL: db.CanonicalChannelURL(*req.ChannelURL), Name: *req.Name, AvatarURL: *req.AvatarURL, SubscribedAt: time.Now().UnixMilli()}
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO subscriptions (user_id, channel_url, name, avatar_url, subscribed_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, channel_url) DO UPDATE SET name = EXCLUDED.name, avatar_url = EXCLUDED.avatar_url`, user, it.ChannelURL, it.Name, it.AvatarURL, it.SubscribedAt); err != nil {
			s.fail(w, r, err)
			return
		}
		s.feedChanged(user)
		httpx.JSON(w, http.StatusCreated, it)
	}))
	rt.Handle("DELETE", "/subscriptions", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		u := r.URL.Query().Get("url")
		if strings.TrimSpace(u) == "" {
			plain(w, r, http.StatusBadRequest, "Missing channelUrl")
			return
		}
		s.unsubscribe(w, r, user, u)
	}))
	rt.Handle("DELETE", "/subscriptions/{channelUrl...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		u := r.URL.Query().Get("url")
		if strings.TrimSpace(u) == "" {
			raw := strings.TrimPrefix(r.URL.EscapedPath()[strings.Index(r.URL.EscapedPath(), "/subscriptions/")+len("/subscriptions/"):], "")
			if d, err := url.QueryUnescape(raw); err == nil {
				raw = d
			}
			u = schemeSlash.ReplaceAllString(raw, "$1://$2")
		}
		if strings.TrimSpace(u) == "" {
			plain(w, r, http.StatusBadRequest, "Missing channelUrl")
			return
		}
		s.unsubscribe(w, r, user, u)
	}))
	s.progressRoutes(rt)
	s.mediaListRoutes(rt)
}

func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request, user, raw string) {
	c := db.CanonicalChannelURL(raw)
	s.DB.Exec(r.Context(), "DELETE FROM subscription_group_memberships WHERE user_id = $1 AND channel_url = $2", user, c)
	tag, err := s.DB.Exec(r.Context(), "DELETE FROM subscriptions WHERE user_id = $1 AND channel_url = $2", user, c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		plain(w, r, http.StatusNotFound, "Not found")
		return
	}
	s.feedChanged(user)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) subscriptionList(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	group, hasGroup := q.Get("groupId"), q.Has("groupId")
	if hasGroup && strings.TrimSpace(group) == "" {
		plain(w, r, http.StatusBadRequest, "Invalid subscription filter")
		return
	}
	ungrouped := false
	if q.Has("ungrouped") {
		switch q.Get("ungrouped") {
		case "true":
			ungrouped = true
		case "false":
		default:
			plain(w, r, http.StatusBadRequest, "Invalid subscription filter")
			return
		}
	}
	if hasGroup && ungrouped {
		plain(w, r, http.StatusBadRequest, "Invalid subscription filter")
		return
	}
	if hasGroup {
		var ok bool
		s.DB.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM subscription_groups WHERE user_id = $1 AND id = $2)", user, group).Scan(&ok)
		if !ok {
			httpx.Error(w, r, http.StatusNotFound, "Subscription group not found", "subscription_group_not_found")
			return
		}
	}
	items, err := s.subscriptions(r, user, group, ungrouped, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, items)
}

// subscriptions lists a user's channels, newest first, filling missing avatars from the videos they watched or
// saved and remembering the repair, like SubscriptionAvatarRepairer.
func (s *Server) subscriptions(r *http.Request, user, group string, ungrouped, withGroups bool) ([]subscriptionItem, error) {
	ctx := r.Context()
	s.DB.Exec(ctx, `UPDATE subscriptions s SET avatar_url = src.avatar FROM (
  SELECT DISTINCT ON (channel_url) channel_url, channel_avatar AS avatar FROM (
    SELECT channel_url, channel_avatar FROM history WHERE user_id = $1 AND channel_avatar <> ''
    UNION ALL SELECT channel_url, channel_avatar FROM playlist_videos WHERE user_id = $1 AND channel_avatar <> ''
    UNION ALL SELECT channel_url, channel_avatar FROM favorites WHERE user_id = $1 AND channel_avatar <> '') x) src
WHERE s.user_id = $1 AND s.avatar_url = '' AND s.channel_url = src.channel_url`, user)
	rows, err := s.DB.Query(ctx, "SELECT channel_url, name, avatar_url, subscribed_at FROM subscriptions WHERE user_id = $1 ORDER BY subscribed_at DESC", user)
	if err != nil {
		return nil, err
	}
	var all []subscriptionItem
	for rows.Next() {
		var it subscriptionItem
		if rows.Scan(&it.ChannelURL, &it.Name, &it.AvatarURL, &it.SubscribedAt) == nil {
			all = append(all, it)
		}
	}
	rows.Close()
	groups := map[string][]string{}
	if group != "" || ungrouped || withGroups {
		mrows, err := s.DB.Query(ctx, `SELECT m.channel_url, m.group_id FROM subscription_group_memberships m
JOIN subscription_groups g ON g.id = m.group_id WHERE m.user_id = $1 AND g.user_id = $1 ORDER BY m.group_id`, user)
		if err != nil {
			return nil, err
		}
		for mrows.Next() {
			var u, g string
			if mrows.Scan(&u, &g) == nil {
				c := db.CanonicalChannelURL(u)
				groups[c] = append(groups[c], g)
			}
		}
		mrows.Close()
	}
	out := []subscriptionItem{}
	for _, it := range all {
		gs := groups[db.CanonicalChannelURL(it.ChannelURL)]
		switch {
		case group != "" && !contains(gs, group):
			continue
		case ungrouped && len(gs) > 0:
			continue
		}
		if withGroups {
			ids := append([]string{}, gs...)
			it.GroupIDs = &ids
		}
		out = append(out, it)
	}
	return out, nil
}

func contains(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}

func validVideoURL(u string) bool { return strings.TrimSpace(u) != "" && len(u) <= 2048 }

func (s *Server) progressRoutes(rt *Router) {
	getOne := func(w http.ResponseWriter, r *http.Request, user, u string) {
		var p progressItem
		err := s.DB.QueryRow(r.Context(), "SELECT video_url, position, updated_at FROM progress WHERE user_id = $1 AND video_url = $2", user, u).
			Scan(&p.VideoURL, &p.Position, &p.UpdatedAt)
		if isNoRows(err) {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, p)
	}
	putOne := func(w http.ResponseWriter, r *http.Request, user, u string) {
		var req struct {
			Position *int64 `json:"position"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.Position == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		items, err := s.writeProgress(r, user, map[string]int64{u: *req.Position}, []string{u})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, items[0])
	}
	rt.Handle("GET", "/progress/{videoUrl...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		getOne(w, r, user, urlTail(r, "videoUrl"))
	}))
	rt.Handle("GET", "/progress", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if u := r.URL.Query().Get("url"); strings.TrimSpace(u) != "" {
			getOne(w, r, user, u)
			return
		}
		plain(w, r, http.StatusBadRequest, "Missing videoUrl")
	}))
	rt.Handle("PUT", "/progress/{videoUrl...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		putOne(w, r, user, urlTail(r, "videoUrl"))
	}))
	rt.Handle("PUT", "/progress", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if u := r.URL.Query().Get("url"); strings.TrimSpace(u) != "" {
			putOne(w, r, user, u)
			return
		}
		plain(w, r, http.StatusBadRequest, "Missing videoUrl")
	}))
	rt.Handle("POST", "/progress/batch", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			VideoURLs []string `json:"videoUrls"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if len(req.VideoURLs) == 0 || len(req.VideoURLs) > 200 || !all(req.VideoURLs, validVideoURL) {
			plain(w, r, http.StatusBadRequest, "Invalid videoUrls")
			return
		}
		urls := dedupe(req.VideoURLs)
		got := map[string]progressItem{}
		rows, err := s.DB.Query(r.Context(), "SELECT video_url, position, updated_at FROM progress WHERE user_id = $1 AND video_url = ANY($2)", user, urls)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for rows.Next() {
			var p progressItem
			if rows.Scan(&p.VideoURL, &p.Position, &p.UpdatedAt) == nil {
				got[p.VideoURL] = p
			}
		}
		rows.Close()
		out := make([]progressItem, 0, len(urls))
		for _, u := range urls {
			if p, ok := got[u]; ok {
				out = append(out, p)
			} else {
				out = append(out, progressItem{VideoURL: u})
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("PUT", "/progress/batch", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			Items []struct {
				VideoURL *string `json:"videoUrl"`
				Position *int64  `json:"position"`
			} `json:"items"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		positions, order := map[string]int64{}, []string{}
		for _, it := range req.Items {
			if it.VideoURL == nil || it.Position == nil {
				plain(w, r, http.StatusBadRequest, "Invalid request body")
				return
			}
			if _, seen := positions[*it.VideoURL]; !seen {
				order = append(order, *it.VideoURL)
			}
			positions[*it.VideoURL] = *it.Position
		}
		if len(req.Items) == 0 || len(req.Items) > 200 || !all(order, validVideoURL) {
			plain(w, r, http.StatusBadRequest, "Invalid items")
			return
		}
		items, err := s.writeProgress(r, user, positions, order)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, items)
	}))
}

func all(a []string, ok func(string) bool) bool {
	for _, v := range a {
		if !ok(v) {
			return false
		}
	}
	return true
}

func dedupe(a []string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, v := range a {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// writeProgress stores positions in milliseconds, unless the user switched watch history off: then it only echoes.
func (s *Server) writeProgress(r *http.Request, user string, positions map[string]int64, order []string) ([]progressItem, error) {
	now := time.Now().UnixMilli()
	out := make([]progressItem, 0, len(order))
	for _, u := range order {
		out = append(out, progressItem{VideoURL: u, Position: max(0, positions[u]), UpdatedAt: now})
	}
	st, err := store.GetSettings(r.Context(), s.DB, user)
	if err != nil || st.DisableWatchHistory {
		return out, err
	}
	for _, p := range out {
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO progress (user_id, video_url, position, updated_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, video_url) DO UPDATE SET position = EXCLUDED.position, updated_at = EXCLUDED.updated_at`, user, p.VideoURL, p.Position, p.UpdatedAt); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m mediaItem) favorite() map[string]any {
	return map[string]any{"videoUrl": m.URL, "favoritedAt": m.At, "title": m.Title, "thumbnail": m.Thumbnail, "duration": m.Duration,
		"channelName": m.ChannelName, "channelUrl": m.ChannelURL, "channelAvatar": m.ChannelAvatar, "viewCount": m.ViewCount, "publishedAt": m.PublishedAt}
}

func (m mediaItem) watchLater() map[string]any {
	return map[string]any{"url": m.URL, "title": m.Title, "thumbnail": m.Thumbnail, "duration": m.Duration, "addedAt": m.At,
		"channelName": m.ChannelName, "channelUrl": m.ChannelURL, "channelAvatar": m.ChannelAvatar, "viewCount": m.ViewCount, "publishedAt": m.PublishedAt}
}

const mediaCols = "title, thumbnail, duration, channel_name, channel_url, channel_avatar, view_count, published_at"

func (s *Server) mediaList(r *http.Request, user, table, urlCol, atCol string) ([]mediaItem, error) {
	rows, err := s.DB.Query(r.Context(), "SELECT "+urlCol+", "+atCol+", "+mediaCols+" FROM "+table+" WHERE user_id = $1 ORDER BY "+atCol+" DESC", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []mediaItem{}
	for rows.Next() {
		var m mediaItem
		if err := rows.Scan(&m.URL, &m.At, &m.Title, &m.Thumbnail, &m.Duration, &m.ChannelName, &m.ChannelURL, &m.ChannelAvatar, &m.ViewCount, &m.PublishedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Server) mediaListRoutes(rt *Router) {
	rt.Handle("GET", "/favorites", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		items, err := s.mediaList(r, user, "favorites", "video_url", "favorited_at")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(items))
		for _, m := range items {
			out = append(out, m.favorite())
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	favorite := func(r *http.Request, user, u string) (*mediaItem, error) {
		var m mediaItem
		err := s.DB.QueryRow(r.Context(), "SELECT video_url, favorited_at, "+mediaCols+" FROM favorites WHERE user_id = $1 AND video_url = $2 LIMIT 1", user, u).
			Scan(&m.URL, &m.At, &m.Title, &m.Thumbnail, &m.Duration, &m.ChannelName, &m.ChannelURL, &m.ChannelAvatar, &m.ViewCount, &m.PublishedAt)
		if isNoRows(err) {
			return nil, nil
		}
		return &m, err
	}
	rt.Handle("GET", "/favorites/status", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		u := r.URL.Query().Get("videoUrl")
		if strings.TrimSpace(u) == "" {
			plain(w, r, http.StatusBadRequest, "Missing videoUrl")
			return
		}
		m, err := favorite(r, user, u)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]bool{"isFavorite": m != nil})
	}))
	rt.Handle("GET", "/favorites/{videoUrl...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		m, err := favorite(r, user, urlTail(r, "videoUrl"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if m == nil {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		httpx.JSON(w, http.StatusOK, m.favorite())
	}))
	rt.Handle("POST", "/favorites/{videoUrl...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		m := mediaItem{URL: urlTail(r, "videoUrl"), At: time.Now().UnixMilli(), PublishedAt: -1}
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO favorites (user_id, video_url, favorited_at, `+mediaCols+`)
VALUES ($1, $2, $3, '', '', 0, '', '', '', 0, -1) ON CONFLICT (user_id, video_url) DO UPDATE SET favorited_at = EXCLUDED.favorited_at`, user, m.URL, m.At); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, m.favorite())
	}))
	rt.Handle("DELETE", "/favorites/{videoUrl...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		s.deleteRow(w, r, "DELETE FROM favorites WHERE user_id = $1 AND video_url = $2", user, urlTail(r, "videoUrl"))
	}))
	rt.Handle("GET", "/watch-later", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		items, err := s.mediaList(r, user, "watch_later", "url", "added_at")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(items))
		for _, m := range items {
			out = append(out, m.watchLater())
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("POST", "/watch-later", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var raw map[string]json.RawMessage
		if !decodeOr(w, r, &raw, "Invalid request body") {
			return
		}
		for _, k := range []string{"url", "title", "thumbnail", "duration"} {
			if _, ok := raw[k]; !ok {
				plain(w, r, http.StatusBadRequest, "Invalid request body")
				return
			}
		}
		b, _ := json.Marshal(raw)
		var req struct {
			URL     string `json:"url"`
			AddedAt int64  `json:"addedAt"`
			mediaItemJSON
		}
		req.PublishedAt = -1
		if json.Unmarshal(b, &req) != nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		m := mediaItem{URL: req.URL, At: req.AddedAt, Title: req.Title, Thumbnail: req.Thumbnail, Duration: req.Duration, ChannelName: req.ChannelName,
			ChannelURL: req.ChannelURL, ChannelAvatar: req.ChannelAvatar, ViewCount: req.ViewCount, PublishedAt: req.PublishedAt}
		if m.At <= 0 {
			m.At = time.Now().UnixMilli()
		}
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO watch_later (user_id, url, added_at, `+mediaCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (user_id, url) DO UPDATE SET added_at = EXCLUDED.added_at, title = EXCLUDED.title, thumbnail = EXCLUDED.thumbnail, duration = EXCLUDED.duration,
channel_name = EXCLUDED.channel_name, channel_url = EXCLUDED.channel_url, channel_avatar = EXCLUDED.channel_avatar, view_count = EXCLUDED.view_count,
published_at = EXCLUDED.published_at`, user, m.URL, m.At, m.Title, m.Thumbnail, m.Duration, m.ChannelName, m.ChannelURL, m.ChannelAvatar, m.ViewCount, m.PublishedAt); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, m.watchLater())
	}))
	rt.Handle("DELETE", "/watch-later/{videoUrl...}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		s.deleteRow(w, r, "DELETE FROM watch_later WHERE user_id = $1 AND url = $2", user, urlTail(r, "videoUrl"))
	}))
}

type mediaItemJSON struct {
	Title         string `json:"title"`
	Thumbnail     string `json:"thumbnail"`
	Duration      int64  `json:"duration"`
	ChannelName   string `json:"channelName"`
	ChannelURL    string `json:"channelUrl"`
	ChannelAvatar string `json:"channelAvatar"`
	ViewCount     int64  `json:"viewCount"`
	PublishedAt   int64  `json:"publishedAt"`
}

func (s *Server) deleteRow(w http.ResponseWriter, r *http.Request, q string, args ...any) {
	tag, err := s.DB.Exec(r.Context(), q, args...)
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

// feedChanged drops what the subscription feed derived from the user's channels; the feed rebuilds on next read.
func (s *Server) feedChanged(user string) { s.feeds.invalidate(user) }
