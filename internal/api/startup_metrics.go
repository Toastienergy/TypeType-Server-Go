package api

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
)

// startupReport is what the web player measured from the click (or the page load) to the first frame: each step's
// start and end in milliseconds from that origin. Only numbers and a video id are kept.
type startupReport struct {
	Video        string               `json:"video"`
	Origin       string               `json:"origin"`
	FirstFrameMs float64              `json:"firstFrameMs"`
	Steps        map[string][]float64 `json:"steps"`
	Warmed       bool                 `json:"warmed"`
	Trace        string               `json:"trace"`
}

var (
	startupVideoRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	startupStepRe  = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	startupTraceRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

func (s *Server) startupMetricsRoutes(rt *Router) {
	rt.Handle("POST", "/metrics/startup", s.zones.userData.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		var rep startupReport
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
		if err != nil || json.Unmarshal(body, &rep) != nil || !startupVideoRe.MatchString(rep.Video) || rep.FirstFrameMs < 0 ||
			rep.FirstFrameMs > 600_000 || len(rep.Steps) > 24 {
			plain(w, r, http.StatusBadRequest, "Invalid startup report")
			return
		}
		attrs := []any{"videoId", rep.Video, "origin", originName(rep.Origin), "firstFrameMs", int64(rep.FirstFrameMs), "warmed", rep.Warmed}
		if startupTraceRe.MatchString(rep.Trace) {
			attrs = append(attrs, "trace", rep.Trace)
		}
		names := make([]string, 0, len(rep.Steps))
		for k := range rep.Steps {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			v := rep.Steps[k]
			if !startupStepRe.MatchString(k) || len(v) != 2 {
				continue
			}
			attrs = append(attrs, k, [2]int64{int64(v[0]), int64(v[1])})
		}
		s.Log.Info("client_startup", attrs...)
		w.WriteHeader(http.StatusNoContent)
	}))
}

func originName(o string) string {
	if o == "click" {
		return "click"
	}
	return "page_load"
}
