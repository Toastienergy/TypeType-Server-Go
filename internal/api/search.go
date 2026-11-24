package api

import (
	"errors"
	"net/http"
	"strconv"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

// serviceParam reads ?service= like the Kotlin routes: missing or not a number is one error, unknown another.
func serviceParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.URL.Query().Get("service"))
	if err != nil {
		plain(w, r, http.StatusBadRequest, "Missing or invalid 'service' parameter")
		return 0, false
	}
	if id != 0 && id != 5 && id != 6 {
		plain(w, r, http.StatusBadRequest, "Invalid 'service' parameter")
		return 0, false
	}
	return id, true
}

func (s *Server) searchRoutes(rt *Router) {
	rt.Handle("GET", "/search", s.zones.extraction.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.publicAccess(w, r)
		if !ok {
			return
		}
		q := r.URL.Query().Get("q")
		if !r.URL.Query().Has("q") {
			plain(w, r, http.StatusBadRequest, "Missing 'q' parameter")
			return
		}
		svc, ok := serviceParam(w, r)
		if !ok {
			return
		}
		if svc != 0 {
			notWritten(w, r)
			return
		}
		page, err := s.YT.Search(r.Context(), q, r.URL.Query().Get("contentFilter"), r.URL.Query().Get("nextpage"))
		if errors.Is(err, youtube.ErrBadPage) {
			plain(w, r, http.StatusBadRequest, "Invalid page")
			return
		}
		if err != nil {
			s.Log.Warn("search", "err", err)
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Search failed", "error")
			return
		}
		if user != "" {
			if blocked, err := s.blockedFilter(r, user); err == nil {
				kept := page.Items[:0]
				for _, v := range page.Items {
					if !blocked(v) {
						kept = append(kept, v)
					}
				}
				page.Items = kept
			}
		}
		httpx.JSON(w, http.StatusOK, page)
	}))
	rt.Handle("GET", "/suggestions", s.zones.extraction.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		if !r.URL.Query().Has("query") {
			plain(w, r, http.StatusBadRequest, "Missing 'query' parameter")
			return
		}
		svc, ok := serviceParam(w, r)
		if !ok {
			return
		}
		if svc != 0 {
			notWritten(w, r)
			return
		}
		out, err := s.YT.Suggestions(r.Context(), r.URL.Query().Get("query"))
		if err != nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Suggestions failed", "error")
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("GET", "/trending", s.zones.extraction.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		svc, ok := serviceParam(w, r)
		if !ok {
			return
		}
		if svc != 0 {
			notWritten(w, r)
			return
		}
		out, err := s.YT.Trending(r.Context())
		if err != nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Trending failed", "error")
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
}
