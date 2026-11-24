package api

import (
	"errors"
	"net/http"
	"strings"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

// publicAccess is publicAccessOrRespond: guests pass while the instance allows them, any token must be valid.
func (s *Server) publicAccess(w http.ResponseWriter, r *http.Request) (string, bool) {
	st, err := s.Admin.Get(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return "", false
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		if st.AllowGuest {
			return "", true
		}
		plain(w, r, http.StatusUnauthorized, "Authentication required")
		return "", false
	}
	if !strings.HasPrefix(h, "Bearer ") {
		plain(w, r, http.StatusUnauthorized, "Invalid token")
		return "", false
	}
	id, ok := s.Auth.Verify(r.Context(), strings.TrimPrefix(h, "Bearer "))
	if !ok {
		plain(w, r, http.StatusUnauthorized, "Invalid token")
		return "", false
	}
	if !st.AllowGuest && strings.HasPrefix(id, "guest:") {
		plain(w, r, http.StatusUnauthorized, "Authentication required")
		return "", false
	}
	return id, true
}

func (s *Server) commentRoutes(rt *Router) {
	rt.Handle("GET", "/comments", s.comments("nextpage"))
	rt.Handle("GET", "/comments/replies", s.comments("repliesPage"))
}

func (s *Server) comments(pageParam string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		raw := r.URL.Query().Get("url")
		if raw == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		page := r.URL.Query().Get(pageParam)
		if pageParam == "repliesPage" && page == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'repliesPage' parameter")
			return
		}
		if bilibiliHost(raw) {
			s.bilibiliComments(r.Context(), w, r, raw, page)
			return
		}
		if strings.Contains(strings.ToLower(raw), "nicovideo.jp") || strings.Contains(strings.ToLower(raw), "nico.ms") {
			httpx.JSON(w, http.StatusOK, youtube.CommentsPage{Comments: []youtube.Comment{}, CommentsDisabled: true})
			return
		}
		id := YouTubeID(raw)
		if id == "" {
			plain(w, r, http.StatusBadRequest, "Unsupported comments URL")
			return
		}
		res, err := s.YT.Comments(r.Context(), id, page)
		if errors.Is(err, youtube.ErrBadPage) {
			plain(w, r, http.StatusBadRequest, "Invalid page")
			return
		}
		if err != nil {
			s.Log.Warn("comments", "videoId", id, "err", err)
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not load comments", "error")
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	}
}
