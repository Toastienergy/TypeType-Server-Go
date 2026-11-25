package recommender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// History is RecommenderHistoryService with RecommenderUserData's queries: the watches and favorites the filter
// keeps, sent to the service in chunks under a key that changes when they do.
type History struct {
	db     Querier
	client *Client
	cache  Cache
	cfg    Config
}

func NewHistory(db Querier, client *Client, cache Cache, cfg Config) *History {
	return &History{db: db, client: client, cache: cache, cfg: cfg}
}

const uploadTimeoutFactor = 4

// EnsureSent uploads the history unless the same key was sent within HistoryKeyTTL, and returns that key; empty
// when the filter keeps out both history and favorites, false when an upload failed.
func (h *History) EnsureSent(ctx context.Context, user string, timeout time.Duration, f *DataFilter, force bool) (string, bool) {
	withHistory := f.Uses(CatHistory) || f.Uses(CatShortsHistory)
	withFavorites := f.Uses(CatFavorites)
	if !withHistory && !withFavorites {
		return "", true
	}
	fp, err := h.fingerprint(ctx, user, withHistory, withFavorites)
	if err != nil {
		return "", false
	}
	key := fp
	if d := f.Digest(); d != "" {
		key += "-" + d
	}
	if _, ok, _ := h.cache.Get(ctx, sentKey(key)); ok && !force {
		return key, true
	}
	var watches []HistoryItem
	if withHistory {
		all, err := h.watches(ctx, user, h.cfg.HistoryMaxItems)
		if err != nil {
			return "", false
		}
		watches = f.History(all)
	}
	positions := map[string]int64{}
	if len(watches) > 0 {
		var urls []string
		for _, w := range watches {
			if w.Duration > 0 {
				urls = append(urls, w.URL)
			}
		}
		if positions, err = h.positionsMs(ctx, user, urls); err != nil {
			return "", false
		}
	}
	var likes []FavoriteItem
	if withFavorites {
		all, err := h.likes(ctx, user, h.cfg.FavoritesMaxItems)
		if err != nil {
			return "", false
		}
		likes = f.Favorites(all)
	}
	size := h.cfg.HistoryChunkItems
	parts := max(1, (len(watches)+size-1)/size, (len(likes)+size-1)/size)
	for part := range parts {
		body := HistoryUpload(key, chunk(watches, part, size), positions, chunk(likes, part, size), part, part == parts-1)
		if !h.client.UploadHistory(ctx, body, timeout*uploadTimeoutFactor).OK() {
			return "", false
		}
	}
	h.cache.Set(ctx, sentKey(key), "1", h.cfg.HistoryKeyTTL)
	return key, true
}

func chunk[T any](l []T, part, size int) []T {
	from := min(part*size, len(l))
	return l[from:min(from+size, len(l))]
}

func sentKey(key string) string { return "recommender:history-sent:v1:" + key }

// fingerprint is RecommenderHistoryFingerprint: the counts and newest dates of what is sent, hashed with the user.
func (h *History) fingerprint(ctx context.Context, user string, withHistory, withFavorites bool) (string, error) {
	parts := []string{user}
	if withHistory {
		var n, last int64
		if err := h.db.QueryRow(ctx, "SELECT count(id), COALESCE(max(watched_at), 0) FROM history WHERE user_id = $1", user).Scan(&n, &last); err != nil {
			return "", err
		}
		parts = append(parts, "history", strconv.FormatInt(n, 10), strconv.FormatInt(last, 10))
	}
	if withFavorites {
		var n, last int64
		if err := h.db.QueryRow(ctx, "SELECT count(video_url), COALESCE(max(favorited_at), 0) FROM favorites WHERE user_id = $1", user).Scan(&n, &last); err != nil {
			return "", err
		}
		parts = append(parts, "favorites", strconv.FormatInt(n, 10), strconv.FormatInt(last, 10))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])[:32], nil
}

func (h *History) watches(ctx context.Context, user string, limit int) ([]HistoryItem, error) {
	rows, err := h.db.Query(ctx, `SELECT id, url, title, channel_name, channel_url, duration, progress, watched_at FROM history
WHERE user_id = $1 ORDER BY watched_at DESC, id DESC LIMIT $2`, user, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryItem
	for rows.Next() {
		var it HistoryItem
		if err := rows.Scan(&it.ID, &it.URL, &it.Title, &it.ChannelName, &it.ChannelURL, &it.Duration, &it.Progress, &it.WatchedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (h *History) likes(ctx context.Context, user string, limit int) ([]FavoriteItem, error) {
	rows, err := h.db.Query(ctx, `SELECT video_url, favorited_at, title, channel_name, channel_url FROM favorites WHERE user_id = $1
ORDER BY favorited_at DESC LIMIT $2`, user, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FavoriteItem
	for rows.Next() {
		var it FavoriteItem
		if err := rows.Scan(&it.VideoURL, &it.FavoritedAt, &it.Title, &it.ChannelName, &it.ChannelURL); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (h *History) positionsMs(ctx context.Context, user string, urls []string) (map[string]int64, error) {
	out := map[string]int64{}
	for from := 0; from < len(urls); from += 1000 {
		rows, err := h.db.Query(ctx, "SELECT video_url, position FROM progress WHERE user_id = $1 AND video_url = ANY($2)",
			user, urls[from:min(from+1000, len(urls))])
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var u string
			var p int64
			if err := rows.Scan(&u, &p); err != nil {
				rows.Close()
				return nil, err
			}
			out[u] = p
		}
		rows.Close()
	}
	return out, nil
}

// HistoryUpload is RecommenderSnapshotBuilder.historyUpload.
func HistoryUpload(key string, history []HistoryItem, positionsMs map[string]int64, favorites []FavoriteItem, part int, last bool) map[string]any {
	h := make([]any, 0, len(history))
	for _, it := range history {
		o := map[string]any{"id": "history:" + it.ID, "video_url": it.URL, "channel_url": it.ChannelURL,
			"channel_name": it.ChannelName, "title": it.Title}
		if it.Duration > 0 {
			o["duration_s"] = it.Duration
			o["progress_s"] = max(it.Progress, positionsMs[it.URL]/1000)
		}
		o["watched_at"] = it.WatchedAt
		o["is_short"] = isShortsURL(it.URL)
		h = append(h, o)
	}
	fav := make([]any, 0, len(favorites))
	for _, it := range favorites {
		o := map[string]any{"id": "favorite:" + it.VideoURL, "video_url": it.VideoURL, "channel_url": it.ChannelURL, "title": it.Title}
		if it.FavoritedAt > 0 {
			o["added_at"] = it.FavoritedAt
		}
		o["is_short"] = isShortsURL(it.VideoURL)
		fav = append(fav, o)
	}
	return map[string]any{"key": key, "part": part, "last": last, "history": h, "favorites": fav}
}
