package api

import (
	"net/http"
	"strings"

	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/store"
	"typetype.dev/server/internal/youtube"
)

func (s *Server) settings(r *http.Request, user string) (store.Settings, error) {
	return store.GetSettings(r.Context(), s.DB, user)
}

// blockedFilter answers whether a video is hidden for the user: a blocked video, a blocked channel, or a blocked
// keyword in its title (NFKC, case-insensitive), the user's own entries and the global ones.
func (s *Server) blockedFilter(r *http.Request, user string) (func(youtube.VideoItem) bool, error) {
	videos, channels := map[string]bool{}, map[string]bool{}
	var keywords []string
	rows, err := s.DB.Query(r.Context(), `SELECT 'v', video_url FROM blocked_videos WHERE user_id = $1 OR scope = 'global'
UNION ALL SELECT 'c', channel_url FROM blocked_channels WHERE user_id = $1 OR scope = 'global'
UNION ALL SELECT 'k', keyword FROM blocked_keywords WHERE user_id = $1 OR scope = 'global'`, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind, v string
		if rows.Scan(&kind, &v) != nil {
			continue
		}
		switch kind {
		case "v":
			videos[v] = true
			if id := store.VideoID(v); id != "" {
				videos["id:"+id] = true
			}
		case "c":
			channels[db.CanonicalChannelURL(v)] = true
		case "k":
			if n := normalizeKeyword(v); n != "" {
				keywords = append(keywords, n)
			}
		}
	}
	rows.Close()
	if len(videos)+len(channels)+len(keywords) == 0 {
		return func(youtube.VideoItem) bool { return false }, nil
	}
	return func(v youtube.VideoItem) bool {
		if videos[v.URL] || videos["id:"+store.VideoID(v.URL)] || channels[db.CanonicalChannelURL(v.UploaderURL)] {
			return true
		}
		title := normalizeKeyword(v.Title)
		for _, k := range keywords {
			if strings.Contains(title, k) {
				return true
			}
		}
		return false
	}, nil
}
