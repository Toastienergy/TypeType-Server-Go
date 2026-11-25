package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/recommend"
	"typetype.dev/server/internal/recommender"
	"typetype.dev/server/internal/youtube"
)

type recRequest struct {
	svc, limit int
	cursor     recommend.Cursor
	intent     recommend.Intent
}

// parseRecRequest reads the built-in mix's parameters; on the home page a recommender cursor ("rec.") left over from a
// page the recommender no longer serves starts the built-in mix from its first page.
func parseRecRequest(w http.ResponseWriter, r *http.Request, home bool) (recRequest, bool) {
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
	raw := q.Get("cursor")
	if home && recommender.IsCursor(raw) {
		raw = ""
	}
	c, ok := recommend.DecodeCursor(raw)
	if !ok {
		plain(w, r, http.StatusBadRequest, "Invalid 'cursor' parameter")
		return req, false
	}
	req.cursor = c
	return req, true
}

func (s *Server) recommendationRoutes(rt *Router) {
	rt.Handle("GET", "/recommendations/home", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		req, ok := parseRecRequest(w, r, true)
		if !ok {
			return
		}
		if req.svc == 0 {
			if page := s.rec.home.Get(r.Context(), user, req.limit, r.URL.Query().Get("cursor"), recLocale(r), recTimezone(r)); page != nil {
				s.writeRecommenderPage(w, r, user, page)
				return
			}
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
		var preparing any
		if s.rec.home.Preparing(user) {
			preparing = true
		}
		s.writeRecPage(w, r, user, page, true, nil, preparing)
	}))
	rt.Handle("GET", "/recommendations/shorts", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		req, ok := parseRecRequest(w, r, false)
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
		s.writeRecPage(w, r, user, page, false, debug, nil)
	}))
}

func (s *Server) writeRecPage(w http.ResponseWriter, r *http.Request, user string, page recommend.Page, dropFuture bool, debug, preparing any) {
	items, err := s.homeItems(r, user, page.Items, dropFuture)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var next *string
	if page.NextCursor != "" {
		next = &page.NextCursor
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next, "hasMore": next != nil, "debug": debug,
		"algorithm": nil, "rows": nil, "preparing": preparing})
}

// writeRecommenderPage answers with a page of the external recommender, through the same filters, keeping in each
// row only the videos left on this page.
func (s *Server) writeRecommenderPage(w http.ResponseWriter, r *http.Request, user string, page *recommender.HomePage) {
	items, err := s.homeItems(r, user, page.Items, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	shown := map[string]bool{}
	for _, v := range items {
		shown[v.URL] = true
	}
	rows := []recommender.Row{}
	for _, row := range page.Rows {
		var urls []string
		for _, u := range row.VideoURLs {
			if shown[u] {
				urls = append(urls, u)
			}
		}
		if len(urls) > 0 {
			row.VideoURLs = urls
			rows = append(rows, row)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": page.NextCursor, "hasMore": page.HasMore, "debug": nil,
		"algorithm": page.Algorithm, "rows": rows, "preparing": nil})
}

// homeItems drops blocked and not allowed videos, and those outside the focus; studied channels come first.
func (s *Server) homeItems(r *http.Request, user string, videos []youtube.VideoItem, dropFuture bool) ([]youtube.VideoItem, error) {
	hidden, err := s.blockedFilter(r, user)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	focus, access := s.focusFor(r.Context(), user), s.accessFor(r.Context(), user)
	items := []youtube.VideoItem{}
	for _, v := range videos {
		if hidden(v) || dropFuture && startsLater(v, now) || focus != nil && !focus.keepsFeedVideo(v) || !access.allowsUploader(v.UploaderURL, v.UploaderName) {
			continue
		}
		items = append(items, v)
	}
	if focus != nil {
		sort.SliceStable(items, func(i, j int) bool {
			return focus.isStudy(items[i].UploaderURL) && !focus.isStudy(items[j].UploaderURL)
		})
	}
	return items, nil
}

func startsLater(v youtube.VideoItem, now int64) bool {
	at := v.Uploaded
	if v.PublishedAt != nil && *v.PublishedAt > 0 {
		at = *v.PublishedAt
	}
	return at > 0 && at > now
}
