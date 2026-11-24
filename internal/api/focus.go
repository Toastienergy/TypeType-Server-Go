package api

import (
	"net/http"
	"time"

	"typetype.dev/server/internal/httpx"
)

type focusItem struct {
	Enabled              bool     `json:"enabled"`
	EntertainmentGroupID *string  `json:"entertainmentGroupId"`
	StudyGroupID         *string  `json:"studyGroupId"`
	Schedule             string   `json:"schedule"`
	Timezone             *string  `json:"timezone"`
	TransitionDays       int      `json:"transitionDays"`
	ArchiveDays          int      `json:"archiveDays"`
	StrictBlocking       bool     `json:"strictBlocking"`
	GrayscaleThumbnails  bool     `json:"grayscaleThumbnails"`
	StartedAt            *int64   `json:"startedAt"`
	DisableAt            *int64   `json:"disableAt"`
	EntertainmentShare   float64  `json:"entertainmentShare"`
	Active               bool     `json:"active"`
	StudyChannelKeys     []string `json:"studyChannelKeys"`
}

// focusShare is FocusProfile.share: the entertainment share falls linearly from 1 to 0 over the transition days.
func focusShare(startedAt int64, days int, now int64) float64 {
	if days <= 0 {
		return 0
	}
	elapsed := float64(now-startedAt) / float64(days*24*3600*1000)
	return max(0, min(1, 1-elapsed))
}

func (s *Server) focusRoutes(rt *Router) {
	rt.Handle("GET", "/focus", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		now := time.Now().UnixMilli()
		it := focusItem{Schedule: "always", TransitionDays: 60, ArchiveDays: 7, EntertainmentShare: 1, StudyChannelKeys: []string{}}
		var started int64
		err := s.DB.QueryRow(r.Context(), `SELECT enabled, entertainment_group_id, study_group_id, transition_days, archive_days, strict_blocking,
grayscale_thumbnails, schedule, timezone, started_at, disable_at FROM focus_settings WHERE user_id = $1`, user).Scan(&it.Enabled,
			&it.EntertainmentGroupID, &it.StudyGroupID, &it.TransitionDays, &it.ArchiveDays, &it.StrictBlocking, &it.GrayscaleThumbnails,
			&it.Schedule, &it.Timezone, &started, &it.DisableAt)
		if err != nil && !isNoRows(err) {
			s.fail(w, r, err)
			return
		}
		if it.Enabled && it.DisableAt != nil && now >= *it.DisableAt {
			s.DB.Exec(r.Context(), "UPDATE focus_settings SET enabled = FALSE, disable_at = NULL, updated_at = $2 WHERE user_id = $1", user, now)
			it.Enabled, it.DisableAt = false, nil
		}
		if it.Enabled {
			it.StartedAt = &started
			it.EntertainmentShare = focusShare(started, it.TransitionDays, now)
			it.Active = it.EntertainmentGroupID != nil && it.Schedule == "always"
		}
		httpx.JSON(w, http.StatusOK, it)
	}))
}
