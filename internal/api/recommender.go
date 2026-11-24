package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"typetype.dev/server/internal/config"
	"typetype.dev/server/internal/httpx"
)

// The external recommender (TypeType-Signals) is offered only when RECOMMENDER_SERVICE_URL is set and the admin
// turned it on; until the Go relay to it is written, an instance that offers it answers 501 on these routes.
func (s *Server) recommenderOffered(w http.ResponseWriter, r *http.Request) bool {
	st, err := s.Admin.Get(r.Context())
	if config.TrimmedURL("RECOMMENDER_SERVICE_URL") == "" || err != nil || !st.RecommenderEnabled {
		plain(w, r, http.StatusNotFound, "recommender not available")
		return false
	}
	notWritten(w, r)
	return false
}

func (s *Server) recommenderRoutes(rt *Router) {
	rt.Handle("GET", "/version/recommender", func(w http.ResponseWriter, r *http.Request) {
		if config.TrimmedURL("RECOMMENDER_SERVICE_URL") == "" {
			plain(w, r, http.StatusNotFound, "recommender not installed")
			return
		}
		notWritten(w, r)
	})
	offered := s.authed(func(w http.ResponseWriter, r *http.Request, _ string) { s.recommenderOffered(w, r) })
	for _, rt2 := range [][2]string{{"GET", "/recommendations/algorithms"}, {"GET", "/recommendations/settings"}, {"PUT", "/recommendations/settings"},
		{"GET", "/recommendations/data"}, {"GET", "/recommendations/last-home"}, {"GET", "/recommendations/profile"},
		{"GET", "/recommendations/feedback"}, {"POST", "/recommendations/feedback"}, {"DELETE", "/recommendations/feedback/{id}"}} {
		rt.Handle(rt2[0], rt2[1], offered)
	}
}

func (s *Server) recommenderAdminRoutes(rt *Router) {
	rt.Handle("GET", "/admin/recommender/status", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		if config.TrimmedURL("RECOMMENDER_SERVICE_URL") != "" {
			notWritten(w, r)
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
