package api

import (
	"context"
	"net/http"
	"slices"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/recommender"
	"typetype.dev/server/internal/store"
)

// recommenderUserRoutes are RecommenderRoutes: the algorithms, the user's settings, data, last page and profile.
func (s *Server) recommenderUserRoutes(rt *Router) {
	rt.Handle("GET", "/recommendations/algorithms", s.authed(func(w http.ResponseWriter, r *http.Request, _ string) {
		if !s.recommenderOffered(w, r) {
			return
		}
		list := s.rec.catalog.List(r.Context(), recLocale(r))
		if list == nil {
			plain(w, r, http.StatusBadGateway, "recommender unavailable")
			return
		}
		httpx.JSON(w, http.StatusOK, list)
	}))
	rt.Handle("GET", "/recommendations/settings", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !s.recommenderOffered(w, r) {
			return
		}
		st, err := s.rec.settings.Get(r.Context(), user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, st)
	}))
	rt.Handle("PUT", "/recommendations/settings", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !s.recommenderOffered(w, r) {
			return
		}
		var given recommender.Settings
		if !decode(w, r, &given) {
			return
		}
		if given.AlgorithmID != nil && !slices.Contains(s.rec.catalog.OfferedIDs(r.Context(), recLocale(r)), *given.AlgorithmID) {
			plain(w, r, http.StatusBadRequest, "Unknown 'algorithmId'")
			return
		}
		saved, err := s.rec.settings.Upsert(r.Context(), user, given)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.rec.home.Forget(r.Context(), user)
		httpx.JSON(w, http.StatusOK, saved)
	}))
	rt.Handle("GET", "/recommendations/data", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !s.recommenderOffered(w, r) {
			return
		}
		st, err := s.rec.settings.Get(r.Context(), user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		overview, err := recommender.Overview(r.Context(), s.DB, s.recDataSources(), s.rec.feedback, user, st)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, overview)
	}))
	rt.Handle("GET", "/recommendations/last-home", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !s.recommenderOffered(w, r) {
			return
		}
		trace := s.rec.home.LastTrace(r.Context(), user)
		if trace == nil {
			plain(w, r, http.StatusNotFound, "No page built yet")
			return
		}
		httpx.JSON(w, http.StatusOK, trace)
	}))
	rt.Handle("GET", "/recommendations/profile", s.authed(s.recommenderProfile))
}

func (s *Server) recommenderProfile(w http.ResponseWriter, r *http.Request, user string) {
	if !s.recommenderOffered(w, r) {
		return
	}
	ctx := r.Context()
	st, err := s.rec.settings.Get(ctx, user)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !st.Enabled {
		httpx.Error(w, r, http.StatusForbidden, "Turn on recommendations to view this profile", "recommender_disabled")
		return
	}
	algorithm := s.rec.catalog.DefaultID(ctx, recLocale(r))
	if st.AlgorithmID != nil {
		algorithm = *st.AlgorithmID
	}
	var profile map[string]any
	if algorithm != "" {
		profile = recommender.Profile(ctx, s.rec.client, s.rec.snapshots, recommender.PageRequest{User: user, AlgorithmID: algorithm,
			Settings: st, Locale: recLocale(r), Timezone: recTimezone(r), Timeout: s.rec.requestTimeout(ctx, s.recommenderAdmin)})
	}
	if profile == nil {
		plain(w, r, http.StatusBadGateway, "recommender unavailable")
		return
	}
	avatars := map[string]string{}
	if rows, err := s.DB.Query(ctx, "SELECT channel_url, avatar_url FROM subscriptions WHERE user_id = $1 AND avatar_url <> ''", user); err == nil {
		for rows.Next() {
			var u, a string
			if rows.Scan(&u, &a) == nil {
				avatars[u] = a
			}
		}
		rows.Close()
	}
	httpx.JSON(w, http.StatusOK, recommender.Illustrate(ctx, s.DB, user, avatars, profile))
}

// recommenderFeedbackRoutes are RecommenderFeedbackRoutes; a "not for me" also leaves the pages already built.
func (s *Server) recommenderFeedbackRoutes(rt *Router) {
	rt.Handle("GET", "/recommendations/feedback", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !s.recommenderOffered(w, r) {
			return
		}
		list, err := s.rec.feedback.List(r.Context(), user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, list)
	}))
	rt.Handle("POST", "/recommendations/feedback", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !s.recommenderOffered(w, r) {
			return
		}
		var req recommender.Feedback
		if !decode(w, r, &req) {
			return
		}
		saved, err := s.rec.feedback.Add(r.Context(), user, req)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if saved == nil {
			plain(w, r, http.StatusBadRequest, "Invalid 'kind' or 'verdict'")
			return
		}
		if saved.VideoURL != nil {
			s.rec.home.HideVideo(r.Context(), user, *saved.VideoURL, true)
		}
		httpx.JSON(w, http.StatusCreated, saved)
	}))
	rt.Handle("DELETE", "/recommendations/feedback/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !s.recommenderOffered(w, r) {
			return
		}
		removed, err := s.rec.feedback.Delete(r.Context(), user, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if removed == nil {
			plain(w, r, http.StatusNotFound, "Not found")
			return
		}
		if removed.VideoURL != nil {
			s.rec.home.HideVideo(r.Context(), user, *removed.VideoURL, false)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (s *Server) recDataSources() recommender.DataSources {
	return recommender.DataSources{
		Subscriptions: func(ctx context.Context, user string) ([]recommender.Dated, error) {
			rows, err := s.DB.Query(ctx, "SELECT channel_url, name, subscribed_at FROM subscriptions WHERE user_id = $1", user)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var out []recommender.Dated
			for rows.Next() {
				var d recommender.Dated
				if err := rows.Scan(&d.Ref, &d.Title, &d.At); err != nil {
					return nil, err
				}
				out = append(out, d)
			}
			return out, rows.Err()
		},
		BlockedOwn: func(ctx context.Context, user string) ([]recommender.Dated, error) {
			var out []recommender.Dated
			for _, kind := range []string{"channels", "videos"} {
				items, err := s.blockedList(ctx, user, kind)
				if err != nil {
					return nil, err
				}
				for _, it := range items {
					if it.Global != nil && *it.Global {
						continue
					}
					title := it.URL
					if it.Name != nil {
						title = *it.Name
					}
					out = append(out, recommender.Dated{Ref: it.URL, Title: title, At: it.BlockedAt})
				}
			}
			return out, nil
		},
		WatchHistoryDisabled: func(ctx context.Context, user string) bool {
			st, err := store.GetSettings(ctx, s.DB, user)
			return err == nil && st.DisableWatchHistory
		},
	}
}
