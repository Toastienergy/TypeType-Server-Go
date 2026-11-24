package api

import (
	"errors"
	"net/http"
	"strings"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

func (s *Server) channelRoutes(rt *Router) {
	rt.Handle("GET", "/channel", s.zones.channel.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("url")
		if raw == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		s.channelPage(w, r, raw, r.URL.Query().Get("nextpage"))
	}))
	rt.Handle("POST", "/channel/page", s.zones.channel.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL      string `json:"url"`
			NextPage string `json:"nextpage"`
			Sort     string `json:"sort"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if strings.TrimSpace(req.URL) == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		s.channelPageSorted(w, r, req.URL, req.NextPage, req.Sort)
	}))
	rt.Handle("GET", "/channel/posts", s.zones.channel.Limit(nil, s.channelPosts))
	rt.Handle("GET", "/channel/playlists", s.zones.channel.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("url")
		if raw == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		tab := r.URL.Query().Get("tab")
		if tab == "" {
			tab = "playlists"
		}
		if tab != "playlists" && tab != "courses" {
			plain(w, r, http.StatusBadRequest, "Invalid 'tab' parameter")
			return
		}
		out, err := s.YT.ChannelPlaylists(r.Context(), raw, tab, r.URL.Query().Get("nextpage"))
		if errors.Is(err, youtube.ErrNotChannel) || errors.Is(err, youtube.ErrBadPage) {
			plain(w, r, http.StatusBadRequest, "Invalid channel URL")
			return
		}
		if err != nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not load this channel", "error")
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
}

func (s *Server) channelPage(w http.ResponseWriter, r *http.Request, raw, page string) {
	s.channelPageSorted(w, r, raw, page, "")
}

func (s *Server) channelPageSorted(w http.ResponseWriter, r *http.Request, raw, page, sort string) {
	{
		user, ok := s.publicAccess(w, r)
		if !ok {
			return
		}
		if strings.Contains(strings.ToLower(raw), "space.bilibili.com/") {
			ch, ok := s.bilibiliChannel(r.Context(), w, r, raw, page, sort)
			if !ok {
				return
			}
			s.writeChannel(w, r, user, ch)
			return
		}
		if l := strings.ToLower(raw); strings.Contains(l, "nicovideo.jp/") {
			ch, ok := s.niconicoChannel(r.Context(), w, r, raw, page)
			if !ok {
				return
			}
			s.writeChannel(w, r, user, ch)
			return
		}
		ch, err := s.YT.Channel(r.Context(), raw, page)
		switch {
		case errors.Is(err, youtube.ErrNotChannel), errors.Is(err, youtube.ErrBadPage):
			plain(w, r, http.StatusBadRequest, "Invalid channel URL")
			return
		case err != nil:
			s.Log.Warn("channel", "url", raw, "err", err)
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not load this channel", "error")
			return
		}
		s.writeChannel(w, r, user, ch)
	}
}

func (s *Server) writeChannel(w http.ResponseWriter, r *http.Request, user string, ch *youtube.Channel) {
	if user != "" {
		if blocked, err := s.blockedFilter(r, user); err == nil {
			kept := ch.Videos[:0]
			for _, v := range ch.Videos {
				if !blocked(v) {
					kept = append(kept, v)
				}
			}
			ch.Videos = kept
		}
	}
	httpx.JSON(w, http.StatusOK, ch)
}
