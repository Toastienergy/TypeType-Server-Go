package api

import (
	"errors"
	"net/http"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

func (s *Server) channelRoutes(rt *Router) {
	rt.Handle("GET", "/channel", s.zones.channel.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		raw := r.URL.Query().Get("url")
		if raw == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		ch, err := s.YT.Channel(r.Context(), raw, r.URL.Query().Get("nextpage"))
		switch {
		case errors.Is(err, youtube.ErrNotChannel), errors.Is(err, youtube.ErrBadPage):
			plain(w, r, http.StatusBadRequest, "Invalid channel URL")
			return
		case err != nil:
			s.Log.Warn("channel", "url", raw, "err", err)
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not load this channel", "error")
			return
		}
		httpx.JSON(w, http.StatusOK, ch)
	}))
}
