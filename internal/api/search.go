package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

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
			var f *extractFail
			if page, f = s.cachedYoutubeSearch(r.Context(), q, sel, next); f != nil {
				if f.bad {
					plain(w, r, f.Status, f.Message)
				} else {
					httpx.Error(w, r, f.Status, f.Message, f.Code)
				}
				return
			}
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

// cachedYoutubeSearch is CachedSearchService for YouTube: ten minutes for a first page, five for the next ones, an
// empty first page kept 30 s at most.
func (s *Server) cachedYoutubeSearch(ctx context.Context, q string, sel searchSelection, next string) (youtube.SearchPage, *extractFail) {
	var sorts []string
	for _, it := range sel.sorts {
		sorts = append(sorts, it.name)
	}
	slices.Sort(sorts)
	ttl := int64(600)
	if next != "" {
		ttl = 300
	}
	area := cacheArea[youtube.SearchPage]{name: "search", admit: func(p youtube.SearchPage) admission {
		return firstPageAdmission(next, len(p.Items) == 0 && len(p.Playlists) == 0 && len(p.Channels) == 0, ttl)
	}}
	key := publicCacheKey("search-v4", append([]string{"0", q, next, sel.content.name}, sorts...)...)
	return publicCached(s, ctx, area, key, func(ctx context.Context) (youtube.SearchPage, *extractFail) {
		var page youtube.SearchPage
		var err error
		if sel.content.field == ytMusic || strings.HasPrefix(next, "ytm1.") {
			page, err = s.YT.MusicSearch(ctx, q, sel.content.name, sel.content.music, next)
		} else {
			page, err = s.YT.Search(ctx, q, youtubeSP(sel.content, sel.sorts), next)
		}
		switch {
		case errors.Is(err, youtube.ErrBadPage):
			return page, &extractFail{Status: http.StatusBadRequest, Message: "Invalid page", bad: true}
		case err != nil:
			s.Log.Warn("search", "err", err)
			return page, &extractFail{Status: http.StatusUnprocessableEntity, Message: "Search failed", Code: "error"}
		}
		return page, nil
	})
}
