package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"typetype.dev/server/internal/db"
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
	elapsed := float64(max(0, now-startedAt)) / float64(int64(days)*dayMs)
	return max(0, min(1, 1-elapsed))
}

func (s *Server) focusItem(ctx context.Context, user string, f focusRow, now int64) focusItem {
	it := focusItem{Enabled: f.enabled, EntertainmentGroupID: f.entertainment, StudyGroupID: f.study, Schedule: f.schedule, Timezone: f.timezone,
		TransitionDays: f.transitionDays, ArchiveDays: f.archiveDays, StrictBlocking: f.strict, GrayscaleThumbnails: f.grayscale,
		DisableAt: f.disableAt, EntertainmentShare: 1, StudyChannelKeys: []string{}}
	if f.enabled {
		started := f.startedAt
		it.StartedAt, it.EntertainmentShare = &started, focusShare(f.startedAt, f.transitionDays, now)
	}
	if it.Active = f.active(now); it.Active {
		active, _ := s.partitionStudy(ctx, user, f, now, s.channelsOf(ctx, user, *f.entertainment))
		for k := range active {
			it.StudyChannelKeys = append(it.StudyChannelKeys, k)
		}
		sort.Strings(it.StudyChannelKeys)
	}
	return it
}

func (s *Server) focusRoutes(rt *Router) {
	rt.Handle("GET", "/focus", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		now := time.Now().UnixMilli()
		f, err := s.readFocus(r.Context(), user, now)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, s.focusItem(r.Context(), user, f, now))
	}))
	rt.Handle("PUT", "/focus", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			Enabled              *bool   `json:"enabled"`
			EntertainmentGroupID *string `json:"entertainmentGroupId"`
			StudyGroupID         *string `json:"studyGroupId"`
			Schedule             *string `json:"schedule"`
			Timezone             *string `json:"timezone"`
			TransitionDays       *int    `json:"transitionDays"`
			ArchiveDays          *int    `json:"archiveDays"`
			StrictBlocking       *bool   `json:"strictBlocking"`
			GrayscaleThumbnails  *bool   `json:"grayscaleThumbnails"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.Enabled == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		ctx, now := r.Context(), time.Now().UnixMilli()
		cur, err := s.readFocus(ctx, user, now)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next := cur
		switch {
		case cur.enabled && *req.Enabled:
			next.disableAt = nil
		case cur.enabled:
			if next.disableAt == nil {
				at := now + focusExitDelay
				next.disableAt = &at
			}
		default:
			if req.TransitionDays != nil {
				next.transitionDays = *req.TransitionDays
			}
			if req.ArchiveDays != nil {
				next.archiveDays = *req.ArchiveDays
			}
			if next.transitionDays < 1 || next.transitionDays > 365 || next.archiveDays < 1 || next.archiveDays > 365 {
				httpx.Error(w, r, http.StatusBadRequest, "Days must be between 1 and 365", "focus_invalid_days")
				return
			}
			if req.Schedule != nil {
				next.schedule = *req.Schedule
			}
			if req.Timezone != nil && strings.TrimSpace(*req.Timezone) != "" {
				next.timezone = req.Timezone
			}
			if !validSchedule(next.schedule, next.timezone) {
				httpx.Error(w, r, http.StatusBadRequest, "Choose a schedule and a valid timezone", "focus_invalid_schedule")
				return
			}
			if req.EntertainmentGroupID != nil {
				next.entertainment = req.EntertainmentGroupID
			}
			if req.StudyGroupID != nil {
				next.study = req.StudyGroupID
				if *req.StudyGroupID == "" {
					next.study = nil
				}
			}
			unknown := next.entertainment != nil && !s.groupExists(r, user, *next.entertainment) || next.study != nil && !s.groupExists(r, user, *next.study)
			if unknown || next.study != nil && next.entertainment != nil && *next.study == *next.entertainment || *req.Enabled && next.entertainment == nil {
				httpx.Error(w, r, http.StatusBadRequest, "Choose an existing subscription group", "focus_invalid_group")
				return
			}
			if req.StrictBlocking != nil {
				next.strict = *req.StrictBlocking
			}
			if req.GrayscaleThumbnails != nil {
				next.grayscale = *req.GrayscaleThumbnails
			}
			next.enabled, next.disableAt = *req.Enabled, nil
			if *req.Enabled {
				next.startedAt = now
			}
		}
		if err := s.writeFocus(ctx, user, next, now); err != nil {
			s.fail(w, r, err)
			return
		}
		s.recs.invalidate(ctx, user)
		httpx.JSON(w, http.StatusOK, s.focusItem(ctx, user, next, now))
	}))
	rt.Handle("POST", "/focus/study-channels", s.authed(s.addStudyChannel))
	rt.Handle("DELETE", "/focus/study-channels", s.authed(s.removeStudyChannel))
}

func studyChannelURL(raw string) string {
	t := strings.TrimSpace(raw)
	l := strings.ToLower(t)
	if t == "" || len([]rune(t)) > 512 || !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
		return ""
	}
	return db.CanonicalChannelURL(t)
}

func (s *Server) studyGroup(ctx context.Context, r *http.Request, user string) (focusRow, string, bool) {
	f, err := s.readFocus(ctx, user, time.Now().UnixMilli())
	if err != nil || f.study == nil || !s.groupExists(r, user, *f.study) {
		return f, "", false
	}
	return f, *f.study, true
}

func (s *Server) addStudyChannel(w http.ResponseWriter, r *http.Request, user string) {
	var req struct {
		ChannelURL *string `json:"channelUrl"`
		Name       string  `json:"name"`
		AvatarURL  string  `json:"avatarUrl"`
	}
	if !decodeOr(w, r, &req, "Invalid request body") {
		return
	}
	if req.ChannelURL == nil {
		plain(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	canon := studyChannelURL(*req.ChannelURL)
	if canon == "" {
		httpx.Error(w, r, http.StatusBadRequest, "Invalid channel URL", "focus_invalid_channel")
		return
	}
	ctx, now, key := r.Context(), time.Now().UnixMilli(), channelKey(canon)
	f, group, ok := s.studyGroup(ctx, r, user)
	if !ok {
		httpx.Error(w, r, http.StatusBadRequest, "Select a study group in Focus settings first", "focus_no_study_group")
		return
	}
	if f.enabled && f.entertainment != nil && s.channelsOf(ctx, user, *f.entertainment)[key] {
		httpx.Error(w, r, http.StatusConflict, "Entertainment channels cannot be added to the study group while Focus is active", "focus_entertainment_conflict")
		return
	}
	subscribed := false
	rows, _ := s.DB.Query(ctx, "SELECT channel_url FROM subscriptions WHERE user_id = $1", user)
	for rows != nil && rows.Next() {
		var u string
		if rows.Scan(&u) == nil && channelKey(u) == key {
			subscribed = true
		}
	}
	if rows != nil {
		rows.Close()
	}
	if !subscribed {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = canon
		}
		s.DB.Exec(ctx, "INSERT INTO subscriptions (user_id, channel_url, name, avatar_url, subscribed_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING",
			user, canon, name, strings.TrimSpace(req.AvatarURL), now)
		s.feedChanged(user)
	}
	added := int64(-1)
	mrows, _ := s.DB.Query(ctx, "SELECT channel_url, added_at FROM subscription_group_memberships WHERE group_id = $1 AND user_id = $2", group, user)
	for mrows != nil && mrows.Next() {
		var u string
		var at int64
		if mrows.Scan(&u, &at) == nil && channelKey(u) == key && added < 0 {
			added = at
		}
	}
	if mrows != nil {
		mrows.Close()
	}
	if added < 0 {
		added = now
		if _, err := s.DB.Exec(ctx, "INSERT INTO subscription_group_memberships (group_id, user_id, channel_url, added_at) VALUES ($1, $2, $3, $4)",
			group, user, canon, now); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.recs.invalidate(ctx, user)
	resp := map[string]any{"channelUrl": canon, "status": "active", "unlockAt": nil}
	if f.enabled && added > f.startedAt && now-added < studyAddDelay {
		resp["status"], resp["unlockAt"] = "pending", added+studyAddDelay
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (s *Server) removeStudyChannel(w http.ResponseWriter, r *http.Request, user string) {
	if !r.URL.Query().Has("url") {
		httpx.Error(w, r, http.StatusBadRequest, "Missing 'url' parameter", "focus_invalid_channel")
		return
	}
	canon := studyChannelURL(r.URL.Query().Get("url"))
	if canon == "" {
		httpx.Error(w, r, http.StatusBadRequest, "Invalid channel URL", "focus_invalid_channel")
		return
	}
	ctx, key := r.Context(), channelKey(canon)
	_, group, ok := s.studyGroup(ctx, r, user)
	if !ok {
		httpx.Error(w, r, http.StatusBadRequest, "Select a study group in Focus settings first", "focus_no_study_group")
		return
	}
	var urls []string
	addedAts := map[int64]bool{}
	rows, _ := s.DB.Query(ctx, "SELECT channel_url, added_at FROM subscription_group_memberships WHERE group_id = $1 AND user_id = $2", group, user)
	for rows != nil && rows.Next() {
		var u string
		var at int64
		if rows.Scan(&u, &at) == nil && channelKey(u) == key {
			urls, addedAts[at] = append(urls, u), true
		}
	}
	if rows != nil {
		rows.Close()
	}
	notFound := func() {
		httpx.Error(w, r, http.StatusNotFound, "Channel is not in the study group", "focus_study_channel_not_found")
	}
	if len(urls) == 0 {
		notFound()
		return
	}
	tag, err := s.DB.Exec(ctx, "DELETE FROM subscription_group_memberships WHERE group_id = $1 AND user_id = $2 AND channel_url = ANY($3)", group, user, urls)
	if err != nil || tag.RowsAffected() == 0 {
		notFound()
		return
	}
	var grouped bool
	s.DB.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM subscription_group_memberships WHERE user_id = $1 AND channel_url = ANY($2))", user, urls).Scan(&grouped)
	if !grouped {
		srows, _ := s.DB.Query(ctx, "SELECT channel_url, subscribed_at FROM subscriptions WHERE user_id = $1", user)
		var auto []string
		for srows != nil && srows.Next() {
			var u string
			var at int64
			if srows.Scan(&u, &at) == nil && channelKey(u) == key && addedAts[at] {
				auto = append(auto, u)
			}
		}
		if srows != nil {
			srows.Close()
		}
		if len(auto) > 0 {
			s.DB.Exec(ctx, "DELETE FROM subscriptions WHERE user_id = $1 AND channel_url = ANY($2)", user, auto)
			s.feedChanged(user)
		}
	}
	s.recs.invalidate(ctx, user)
	w.WriteHeader(http.StatusNoContent)
}
