package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/recommender"
)

// recommenderOffered is recommenderOffered: 404 unless the recommender is installed and the admin turned it on.
func (s *Server) recommenderOffered(w http.ResponseWriter, r *http.Request) bool {
	if !s.rec.offered(r.Context(), s.recommenderAdmin) {
		plain(w, r, http.StatusNotFound, "recommender not available")
		return false
	}
	return true
}

func (s *Server) recommenderRoutes(rt *Router) {
	rt.Handle("GET", "/version/recommender", func(w http.ResponseWriter, r *http.Request) {
		var version map[string]any
		if s.rec.cfg.Installed() {
			version = s.rec.client.Version(r.Context())
		}
		if version == nil {
			plain(w, r, http.StatusNotFound, "recommender not installed")
			return
		}
		httpx.JSON(w, http.StatusOK, version)
	})
	s.recommenderUserRoutes(rt)
	s.recommenderFeedbackRoutes(rt)
}

func (s *Server) recommenderAdminRoutes(rt *Router) {
	rt.Handle("GET", "/admin/recommender/status", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		if s.rec.cfg.Installed() {
			admin, err := s.recommenderAdmin(r.Context())
			if err != nil {
				s.fail(w, r, err)
				return
			}
			noStore(w)
			httpx.JSON(w, http.StatusOK, recommender.Status(r.Context(), s.rec.cfg, admin, s.rec.client, s.rec.catalog, s.rec.metrics,
				s.rec.limiter, recLocale(r)))
			return
		}
		noStore(w)
		httpx.JSON(w, http.StatusOK, map[string]any{"installed": false, "enabled": false, "reachable": false, "healthMs": nil, "algorithms": []any{},
			"defaultAlgorithm": nil, "pagesBuilt": 0, "pagesFromCache": 0, "fallbacks": 0, "averageBuildMs": 0, "averageRecommenderMs": 0,
			"averageSourcesMs": 0, "buildMsTotal": 0, "recommenderMsTotal": 0, "sourcesMsTotal": 0, "sourceRequests": 0, "sourceCacheHits": 0,
			"streamRequests": 0, "streamRequestsRefused": 0, "streamRequestsThisMinute": 0, "streamRequestsPaused": false,
			"streamRequestsPerMinuteLimit": 15, "requestTimeoutMs": 2500})
	}))
	rt.Handle("GET", "/admin/recommender/feedback", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		ctx := r.Context()
		users := map[string]int{}
		total := 0
		rows, err := s.DB.Query(ctx, "SELECT COALESCE(algorithm_id, ''), count(*) FROM recommender_user_settings WHERE enabled GROUP BY 1")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for rows.Next() {
			var id string
			var n int
			if rows.Scan(&id, &n) == nil {
				users[id], total = n, total+n
			}
		}
		rows.Close()
		frows, err := s.DB.Query(ctx, `SELECT kind, verdict, comment, video_url, reason, title, channel_name, thumbnail_url, algorithm_id, parameters_json,
created_at FROM recommender_feedback ORDER BY created_at DESC LIMIT 5000`)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		type counts struct{ good, bad, notForMe int }
		verdicts := map[string]*counts{}
		comments := []map[string]any{}
		for frows.Next() {
			var kind, verdict, algo, params string
			var comment, video, reason, title, channel, thumb *string
			var created int64
			if frows.Scan(&kind, &verdict, &comment, &video, &reason, &title, &channel, &thumb, &algo, &params, &created) != nil {
				continue
			}
			c := verdicts[algo]
			if c == nil {
				c = &counts{}
				verdicts[algo] = c
			}
			switch verdict {
			case "good":
				c.good++
			case "bad":
				c.bad++
			case "not_for_me":
				c.notForMe++
			}
			if comment != nil && strings.TrimSpace(*comment) != "" && len(comments) < 100 {
				var p map[string]any
				if json.Unmarshal([]byte(params), &p) != nil {
					p = map[string]any{}
				}
				comments = append(comments, map[string]any{"id": "", "kind": kind, "verdict": verdict, "comment": comment, "videoUrl": video, "reason": reason,
					"title": title, "channelName": channel, "thumbnailUrl": thumb, "algorithmId": algo, "parameters": p, "createdAt": created})
			}
		}
		frows.Close()
		ids := make([]string, 0, len(verdicts))
		for id := range verdicts {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		vs := []map[string]any{}
		for _, id := range ids {
			c := verdicts[id]
			vs = append(vs, map[string]any{"algorithmId": id, "good": c.good, "bad": c.bad, "notForMe": c.notForMe})
		}
		noStore(w)
		httpx.JSON(w, http.StatusOK, map[string]any{"usersEnabled": total, "usersByAlgorithm": users, "verdicts": vs, "comments": comments})
	}))
}
