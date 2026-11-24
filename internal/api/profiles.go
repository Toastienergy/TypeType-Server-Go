package api

import (
	"errors"
	"net/http"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

func (s *Server) profileRoutes(rt *Router) {
	rt.Handle("GET", "/profiles", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		list, err := s.Auth.Profiles.List(r.Context(), user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if list == nil {
			plain(w, r, http.StatusForbidden, "Profiles are unavailable")
			return
		}
		httpx.JSON(w, http.StatusOK, list)
	}))
	rt.Handle("POST", "/profiles", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		name, ok := profileName(w, r)
		if !ok {
			return
		}
		p, err := s.Auth.Profiles.Create(r.Context(), user, name)
		if err == nil {
			httpx.JSON(w, http.StatusCreated, p)
			return
		}
		s.profileError(w, r, err, "Profile operation failed")
	}))
	rt.Handle("PUT", "/profiles/{profileId}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		name, ok := profileName(w, r)
		if !ok {
			return
		}
		p, err := s.Auth.Profiles.Rename(r.Context(), user, r.PathValue("profileId"), name)
		s.profileResult(w, r, p, err)
	}))
	rt.Handle("POST", "/profiles/{profileId}/default", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		p, err := s.Auth.Profiles.SetDefault(r.Context(), user, r.PathValue("profileId"))
		s.profileResult(w, r, p, err)
	}))
	rt.Handle("POST", "/profiles/{profileId}/switch", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		p, err := s.Auth.Profiles.Switch(r.Context(), user, r.PathValue("profileId"))
		if errors.Is(err, auth.ErrProfileNotFound) {
			plain(w, r, http.StatusNotFound, "Profile not found")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		t, err := s.Auth.Issue(r.Context(), p.ID, "")
		if err != nil {
			plain(w, r, http.StatusInternalServerError, "Failed to create session")
			return
		}
		s.setRefreshCookie(w, t.Refresh, false)
		httpx.JSON(w, http.StatusOK, map[string]any{"accessToken": t.Access, "profile": p})
	}))
	rt.Handle("DELETE", "/profiles/{profileId}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		switch err := s.Auth.Profiles.Delete(r.Context(), user, r.PathValue("profileId")); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, auth.ErrCannotDeleteOwner):
			plain(w, r, http.StatusConflict, "The account profile cannot be deleted")
		case errors.Is(err, auth.ErrCannotDeleteActive):
			plain(w, r, http.StatusConflict, "Switch profiles before deleting the active profile")
		case errors.Is(err, auth.ErrProfileNotFound):
			plain(w, r, http.StatusNotFound, "Profile not found")
		default:
			s.fail(w, r, err)
		}
	}))
}

func profileName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Name *string `json:"name"`
	}
	if !decodeOr(w, r, &req, "Invalid request body") {
		return "", false
	}
	if req.Name == nil {
		plain(w, r, http.StatusBadRequest, "Invalid request body")
		return "", false
	}
	return *req.Name, true
}

func (s *Server) profileResult(w http.ResponseWriter, r *http.Request, p auth.AccountProfile, err error) {
	if err == nil {
		httpx.JSON(w, http.StatusOK, p)
		return
	}
	s.profileError(w, r, err, "Profile operation failed")
}

func (s *Server) profileError(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	switch {
	case errors.Is(err, auth.ErrInvalidName):
		plain(w, r, http.StatusBadRequest, "PROFILE_NAME_INVALID")
	case errors.Is(err, auth.ErrProfileNotFound):
		plain(w, r, http.StatusNotFound, "Profile not found")
	default:
		s.fail(w, r, err)
	}
}
