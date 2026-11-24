package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/recommend"
	"typetype.dev/server/internal/youtube"
)

type recRequest struct {
	svc, limit int
	cursor     recommend.Cursor
	intent     recommend.Intent
}

func parseRecRequest(w http.ResponseWriter, r *http.Request) (recRequest, bool) {
	q := r.URL.Query()
	req := recRequest{limit: 20, intent: recommend.ParseIntent(q.Get("intent"))}
	if q.Has("service") {
		n, err := strconv.Atoi(q.Get("service"))
		if err != nil || n != 0 && n != 5 && n != 6 {
			plain(w, r, http.StatusBadRequest, "Invalid 'service' parameter")
			return req, false
		}
		req.svc = n
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		req.limit = max(1, min(n, 60))
	}
	c, ok := recommend.DecodeCursor(q.Get("cursor"))
	if !ok {
		plain(w, r, http.StatusBadRequest, "Invalid 'cursor' parameter")
		return req, false
	}
	req.cursor = c
	return req, true
}

func (s *Server) recommendationRoutes(rt *Router) {
	rt.Handle("GET", "/recommendations/home", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		req, ok := parseRecRequest(w, r)
		if !ok {
			return
		}
		pool, err := s.recs.resolve(r, user, req.svc, req.intent, false)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		c := req.cursor
		if c.Seed == 0 && c.SubIndex == 0 && c.DiscIndex == 0 && c.SubRun == 0 && len(c.RecentURLs) == 0 {
			c.Seed = recommend.NewSeed()
		}
		rotated := recommend.Rotate(*pool, c.Seed)
		page := recommend.Mix(&rotated, c, req.limit, req.intent, time.Now())
		s.writeRecPage(w, r, user, page, true, nil)
	}))
	rt.Handle("GET", "/recommendations/shorts", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		req, ok := parseRecRequest(w, r)
		if !ok {
			return
		}
		pool, err := s.recs.resolve(r, user, req.svc, req.intent, true)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page := recommend.MixShorts(pool, req.cursor, req.limit, req.intent, time.Now())
		var debug any
		if r.URL.Query().Get("debug") == "true" {
			debug = page.Debug()
		}
		s.writeRecPage(w, r, user, page, false, debug)
	}))
}

func (s *Server) writeRecPage(w http.ResponseWriter, r *http.Request, user string, page recommend.Page, dropFuture bool, debug any) {
	hidden, err := s.blockedFilter(r, user)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := time.Now().UnixMilli()
	focus := s.focusFor(r.Context(), user)
	items := []youtube.VideoItem{}
	for _, v := range page.Items {
		if hidden(v) || dropFuture && startsLater(v, now) || focus != nil && !focus.keepsFeedVideo(v) {
			continue
		}
		items = append(items, v)
	}
	if focus != nil {
		sort.SliceStable(items, func(i, j int) bool {
			return focus.isStudy(items[i].UploaderURL) && !focus.isStudy(items[j].UploaderURL)
		})
	}
	var next *string
	if page.NextCursor != "" {
		next = &page.NextCursor
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next, "hasMore": next != nil, "debug": debug,
		"algorithm": nil, "rows": nil, "preparing": nil})
}

func startsLater(v youtube.VideoItem, now int64) bool {
	at := v.Uploaded
	if v.PublishedAt != nil && *v.PublishedAt > 0 {
		at = *v.PublishedAt
	}
	return at > 0 && at > now
}
