package api

import (
	"net/http"

	"typetype.dev/server/internal/config"
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
