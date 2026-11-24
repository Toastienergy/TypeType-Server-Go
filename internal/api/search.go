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
		tree := filtersFor(svc)
		if tree == nil {
			notWritten(w, r)
			return
		}
		sel, ok := selectFilters(w, r, tree)
		if !ok {
			return
		}
		next := r.URL.Query().Get("nextpage")
		var page youtube.SearchPage
		var err error
		switch svc {
		case 0:
			if sel.content.field == ytMusic {
				notWritten(w, r)
				return
			}
			page, err = s.YT.Search(r.Context(), q, youtubeSP(sel.content, sel.sorts), next)
		case 5:
			if page, ok = s.bilibiliSearch(r.Context(), w, r, q, sel, next); !ok {
				return
			}
		case 6:
			if page, ok = s.niconicoSearch(r.Context(), w, r, q, sel, next); !ok {
				return
			}
		}
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
		if access := s.accessFor(r.Context(), user); access != nil {
			page.Items = access.filter(page.Items)
			kept := page.Channels[:0]
			for _, c := range page.Channels {
				if access.allowsChannel(c.URL, c.Name) {
					kept = append(kept, c)
				}
			}
			page.Channels = kept
		}
		if sel.contentGiven {
			// Without one, Bilibili's default is videos, and the channel its aggregated search adds stays.
			kindFilter(&page, sel.content.name)
		}
		httpx.JSON(w, http.StatusOK, page)
	}))
	rt.Handle("GET", "/search/filters", s.searchFilters)
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
		user, ok := s.publicAccess(w, r)
		if !ok {
			return
		}
		svc, ok := serviceParam(w, r)
		if !ok {
			return
		}
		var out []youtube.VideoItem
		var err error
		switch svc {
		case 5:
			out, err = s.bilibiliTrending(r.Context())
		case 6:
			out, err = s.niconicoTrending(r.Context())
		case 0:
			out, err = s.YT.Trending(r.Context())
		default:
			notWritten(w, r)
			return
		}
		if err != nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Trending failed", "error")
			return
		}
		httpx.JSON(w, http.StatusOK, s.accessFor(r.Context(), user).filter(out))
	}))
}
