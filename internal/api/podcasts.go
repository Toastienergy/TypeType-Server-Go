package api

import (
	"net/http"
	"strings"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

// podcastChannelURL drops the query, fragment and /podcasts tab, like toPodcastChannelUrl.
func podcastChannelURL(raw string) string {
	u := strings.TrimSpace(raw)
	u, _, _ = strings.Cut(u, "?")
	u, _, _ = strings.Cut(u, "#")
	u = strings.TrimRight(u, "/")
	if i := strings.Index(strings.ToLower(u), "/podcasts"); i >= 0 {
		u = strings.TrimRight(u[:i], "/")
	}
	return u
}

func (s *Server) podcastRoutes(rt *Router) {
	rt.Handle("GET", "/podcasts", s.zones.extraction.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("url")
		if raw == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		channel := podcastChannelURL(raw)
		if !strings.Contains(channel, "youtube.com") {
			plain(w, r, http.StatusBadRequest, "Podcasts are only supported for YouTube channels")
			return
		}
		list, err := s.YT.ChannelPlaylists(r.Context(), channel, "podcasts", r.URL.Query().Get("nextpage"))
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Unsupported podcast URL")
			return
		}
		name := list.ChannelName
		for i := range list.Playlists {
			if list.Playlists[i].UploaderName == "" {
				list.Playlists[i].UploaderName = name
			}
			list.Playlists[i].PlaylistType = "PODCAST"
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"channelName": name, "channelUrl": channel, "podcasts": list.Playlists,
			"episodes": []youtube.VideoItem{}, "nextpage": list.NextPage})
	}))
	rt.Handle("GET", "/podcasts/episodes", s.zones.extraction.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("url")
		if raw == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		p, err := s.YT.Playlist(r.Context(), raw, r.URL.Query().Get("nextpage"))
		if err != nil {
			s.playlistError(w, r, err)
			return
		}
		p.Playlist.PlaylistType = "PODCAST"
		httpx.JSON(w, http.StatusOK, map[string]any{"podcast": p.Playlist, "episodes": p.Videos, "nextpage": p.NextPage})
	}))
}
