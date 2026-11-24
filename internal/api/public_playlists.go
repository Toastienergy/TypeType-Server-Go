package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

type savedPlaylist struct {
	ID               string `json:"id"`
	PublicPlaylistID string `json:"publicPlaylistId"`
	URL              string `json:"url"`
	Title            string `json:"title"`
	ThumbnailURL     string `json:"thumbnailUrl"`
	UploaderName     string `json:"uploaderName"`
	StreamCount      int64  `json:"streamCount"`
	PlaylistType     string `json:"playlistType"`
	SavedAt          int64  `json:"savedAt"`
}

func (s *Server) playlistError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, youtube.ErrNotPlaylist) || errors.Is(err, youtube.ErrBadPage) {
		plain(w, r, http.StatusBadRequest, "Invalid playlist URL")
		return
	}
	httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not load this playlist", "error")
}

func (s *Server) publicPlaylistRoutes(rt *Router) {
	rt.Handle("GET", "/playlist", s.zones.extraction.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("url")
		if raw == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		p, err := s.YT.Playlist(r.Context(), raw, r.URL.Query().Get("nextpage"))
		if err != nil {
			s.playlistError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, p)
	}))
	rt.Handle("GET", "/saved-playlists", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		rows, err := s.DB.Query(r.Context(), `SELECT id, public_playlist_id, url, title, thumbnail_url, uploader_name, stream_count, playlist_type, saved_at
FROM saved_playlists WHERE user_id = $1 ORDER BY saved_at DESC`, user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		defer rows.Close()
		out := []savedPlaylist{}
		for rows.Next() {
			var p savedPlaylist
			if rows.Scan(&p.ID, &p.PublicPlaylistID, &p.URL, &p.Title, &p.ThumbnailURL, &p.UploaderName, &p.StreamCount, &p.PlaylistType, &p.SavedAt) == nil {
				out = append(out, p)
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("POST", "/saved-playlists", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			URL *string `json:"url"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.URL == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		u := strings.TrimSpace(*req.URL)
		if u == "" {
			plain(w, r, http.StatusBadRequest, "Missing url")
			return
		}
		pl, err := s.YT.Playlist(r.Context(), u, "")
		if err != nil {
			s.playlistError(w, r, err)
			return
		}
		p := pl.Playlist
		sp := savedPlaylist{PublicPlaylistID: p.ID, URL: p.URL, Title: p.Title, ThumbnailURL: p.ThumbnailURL, UploaderName: p.UploaderName,
			StreamCount: p.StreamCount, PlaylistType: p.PlaylistType}
		err = s.DB.QueryRow(r.Context(), "SELECT id, saved_at FROM saved_playlists WHERE user_id = $1 AND url = $2", user, p.URL).Scan(&sp.ID, &sp.SavedAt)
		if isNoRows(err) {
			sp.ID, sp.SavedAt = auth.UUID(), time.Now().UnixMilli()
			err = nil
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO saved_playlists (id, user_id, public_playlist_id, url, title, thumbnail_url, uploader_name,
stream_count, playlist_type, saved_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (id) DO UPDATE SET public_playlist_id = EXCLUDED.public_playlist_id,
title = EXCLUDED.title, thumbnail_url = EXCLUDED.thumbnail_url, uploader_name = EXCLUDED.uploader_name, stream_count = EXCLUDED.stream_count,
playlist_type = EXCLUDED.playlist_type`, sp.ID, user, sp.PublicPlaylistID, sp.URL, sp.Title, sp.ThumbnailURL, sp.UploaderName, sp.StreamCount, sp.PlaylistType, sp.SavedAt); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, sp)
	}))
	rt.Handle("DELETE", "/saved-playlists/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		s.deleteRow(w, r, "DELETE FROM saved_playlists WHERE id = $1 AND user_id = $2", r.PathValue("id"), user)
	}))
}
