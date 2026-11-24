package api

import (
	"context"
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
	id, ok, err := s.Auth.Verify(r.Context(), strings.TrimPrefix(h, "Bearer "))
	if err != nil {
		s.fail(w, r, err)
		return "", false
	}
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
	rt.Handle("GET", "/comments/voice", s.commentVoice)
	rt.Handle("GET", "/comments/search", s.commentSearch)
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
		res, cerr := s.commentPage(r.Context(), raw, page)
		if cerr != nil {
			cerr.write(w, r)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	}
}

type commentErr struct {
	status    int
	msg, code string
}

func (e *commentErr) write(w http.ResponseWriter, r *http.Request) {
	if e.code == "" {
		plain(w, r, e.status, e.msg)
		return
	}
	httpx.Error(w, r, e.status, e.msg, e.code)
}

// commentPage is CommentService.getComments: a Bilibili page, no comments for NicoNico (PipePipe has none), or a
// YouTube page.
func (s *Server) commentPage(ctx context.Context, raw, page string) (youtube.CommentsPage, *commentErr) {
	if bilibiliHost(raw) {
		return s.bilibiliComments(ctx, raw, page)
	}
	if strings.Contains(strings.ToLower(raw), "nicovideo.jp") || strings.Contains(strings.ToLower(raw), "nico.ms") {
		return youtube.CommentsPage{Comments: []youtube.Comment{}, CommentsDisabled: true}, nil
	}
	id := YouTubeID(raw)
	if id == "" {
		return youtube.CommentsPage{}, &commentErr{http.StatusBadRequest, "Unsupported comments URL", ""}
	}
	res, err := s.YT.Comments(ctx, id, page)
	if errors.Is(err, youtube.ErrBadPage) {
		return res, &commentErr{http.StatusBadRequest, "Invalid page", ""}
	}
	if err != nil {
		s.Log.Warn("comments", "videoId", id, "err", err)
		return res, &commentErr{http.StatusUnprocessableEntity, "Could not load comments", "error"}
	}
	return res, nil
}
