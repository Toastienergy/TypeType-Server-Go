package api

import (
	"net/http"
	"strings"

	"typetype.dev/server/internal/store"
	"typetype.dev/server/internal/youtube"
)

func (s *Server) settings(r *http.Request, user string) (store.Settings, error) {
	return store.GetSettings(r.Context(), s.DB, user)
}

// blockedFilter is BlockedContentProfile.allowsVideo inverted: a video blocked by its key, a keyword of its title,
// or its channel, matched by normalized URL or by name, from the user's lists and the global ones.
func (s *Server) blockedFilter(r *http.Request, user string) (func(youtube.VideoItem) bool, error) {
	videos, urls, names := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var keywords []string
	rows, err := s.DB.Query(r.Context(), `SELECT 'v', video_url, '' FROM blocked_videos WHERE user_id = $1 OR scope = 'global'
UNION ALL SELECT 'c', channel_url, COALESCE(name, '') FROM blocked_channels WHERE user_id = $1 OR scope = 'global'
UNION ALL SELECT 'k', keyword, '' FROM blocked_keywords WHERE user_id = $1 OR scope = 'global'`, user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var kind, v, name string
		if rows.Scan(&kind, &v, &name) != nil {
			continue
		}
		switch kind {
		case "v":
			if k := videoKey(v); strings.TrimSpace(k) != "" {
				videos[k] = true
			}
		case "c":
			if k := channelKey(v); strings.TrimSpace(k) != "" {
				urls[k] = true
			}
			if n := normalizeKeyword(name); n != "" {
				names[n] = true
			}
		case "k":
			if n := normalizeKeyword(v); n != "" {
				keywords = append(keywords, n)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(videos)+len(urls)+len(names)+len(keywords) == 0 {
		return func(youtube.VideoItem) bool { return false }, nil
	}
	return func(v youtube.VideoItem) bool {
		if videos[videoKey(v.URL)] || urls[channelKey(v.UploaderURL)] || names[normalizeKeyword(v.UploaderName)] {
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
