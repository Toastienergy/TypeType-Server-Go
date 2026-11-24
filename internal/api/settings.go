package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/store"
)

func (s *Server) settingsRoutes(rt *Router) {
	rt.Handle("GET", "/settings", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		st, err := store.GetSettings(r.Context(), s.DB, user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, st)
	}))
	rt.Handle("PUT", "/settings", s.authed(s.putSettings))
}

// authed is withJwtAuth around a handler that needs the caller.
func (s *Server) authed(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.userID(w, r)
		if ok {
			next(w, r, id)
		}
	}
}

// putSettings merges the fields sent over the stored settings, like the Kotlin JsonObject merge: absent fields keep
// their value, a field of the wrong type (or null) makes the whole request a 400.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, user string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var patch map[string]json.RawMessage
	if err != nil || json.Unmarshal(body, &patch) != nil || patch == nil {
		plain(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	current, err := store.GetSettings(r.Context(), s.DB, user)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	base, _ := json.Marshal(current)
	var merged map[string]json.RawMessage
	json.Unmarshal(base, &merged)
	for k, v := range patch {
		if _, known := merged[k]; !known {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		merged[k] = v
	}
	raw, _ := json.Marshal(merged)
	next := store.DefaultSettings()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&next); err != nil || !next.ValidModes() {
		plain(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	saved, err := store.UpsertSettings(r.Context(), s.DB, user, next)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}
