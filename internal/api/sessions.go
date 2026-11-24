package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionInactivity = 5 * time.Minute
	presenceTTL       = 2 * time.Minute
)

type nowPlaying struct {
	VideoURL    string  `json:"videoUrl"`
	Title       string  `json:"title"`
	Thumbnail   *string `json:"thumbnail"`
	ChannelName *string `json:"channelName"`
	PositionMs  int64   `json:"positionMs"`
	DurationMs  *int64  `json:"durationMs"`
	Paused      bool    `json:"paused"`
	UpdatedAt   int64   `json:"updatedAt"`
}

type activeSession struct {
	ID             string      `json:"id"`
	UserID         string      `json:"userId"`
	ClientName     *string     `json:"clientName"`
	ClientVersion  *string     `json:"clientVersion"`
	DeviceID       *string     `json:"deviceId"`
	DeviceName     *string     `json:"deviceName"`
	DeviceType     *string     `json:"deviceType"`
	UserAgent      *string     `json:"userAgent"`
	LastActivityAt int64       `json:"lastActivityAt"`
	LastPlaybackAt *int64      `json:"lastPlaybackAt"`
	NowPlaying     *nowPlaying `json:"nowPlaying"`
}

// activity keeps who is connected and what plays, in memory only, as ActiveSessionService and PresenceService do:
// the admin view of active sessions and the presence of a user are lost on restart by design.
type activity struct {
	mu       sync.Mutex
	sessions map[string]*activeSession
	presence map[string]nowPlaying
}

func newActivity() *activity {
	return &activity{sessions: map[string]*activeSession{}, presence: map[string]nowPlaying{}}
}

type activityRequest struct {
	ClientName    *string `json:"clientName"`
	ClientVersion *string `json:"clientVersion"`
	DeviceID      *string `json:"deviceId"`
	DeviceName    *string `json:"deviceName"`
	DeviceType    *string `json:"deviceType"`
	VideoURL      *string `json:"videoUrl"`
	Title         *string `json:"title"`
	Thumbnail     *string `json:"thumbnail"`
	ChannelName   *string `json:"channelName"`
	PositionMs    int64   `json:"positionMs"`
	DurationMs    *int64  `json:"durationMs"`
	Paused        bool    `json:"paused"`
}

func text(p *string, limit int) *string {
	if p == nil {
		return nil
	}
	t := strings.TrimSpace(*p)
	if t == "" {
		return nil
	}
	if len(t) > limit {
		t = t[:limit]
	}
	return &t
}

func (s *Server) sessionRoutes(rt *Router) {
	handle := func(kind string) http.HandlerFunc {
		return s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
			var req activityRequest
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
			if err != nil || json.Unmarshal(body, &req) != nil {
				plain(w, r, http.StatusBadRequest, "Invalid request body")
				return
			}
			if kind == "start" && (req.VideoURL == nil || req.Title == nil) {
				plain(w, r, http.StatusBadRequest, "Invalid request body")
				return
			}
			if kind == "start" && (strings.TrimSpace(*req.VideoURL) == "" || strings.TrimSpace(*req.Title) == "") {
				plain(w, r, http.StatusBadRequest, "Missing now playing fields")
				return
			}
			st, err := s.Admin.Get(r.Context())
			if err == nil && st.ActiveSessionsEnabled {
				s.activity.report(user, kind, &req, r.UserAgent())
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	rt.Handle("POST", "/sessions/activity", handle("activity"))
	rt.Handle("POST", "/sessions/playback/start", handle("start"))
	rt.Handle("POST", "/sessions/playback/progress", handle("progress"))
	rt.Handle("POST", "/sessions/playback/stop", handle("stop"))
}

func (a *activity) report(user, kind string, req *activityRequest, ua string) {
	now := time.Now().UnixMilli()
	device := ""
	if req.DeviceID != nil {
		device = *req.DeviceID
	}
	client := ""
	if req.ClientName != nil {
		client = *req.ClientName
	}
	id := user + "|" + device + "|" + client
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range a.sessions {
		if now-v.LastActivityAt > sessionInactivity.Milliseconds() {
			delete(a.sessions, k)
		}
	}
	prev := a.sessions[id]
	rec := &activeSession{ID: id, UserID: user, ClientName: text(req.ClientName, 80), ClientVersion: text(req.ClientVersion, 80),
		DeviceID: text(req.DeviceID, 120), DeviceName: text(req.DeviceName, 120), DeviceType: text(req.DeviceType, 40),
		UserAgent: text(&ua, 300), LastActivityAt: now}
	switch kind {
	case "start", "progress":
		np := nowPlaying{PositionMs: max(0, req.PositionMs), DurationMs: req.DurationMs, Paused: req.Paused, UpdatedAt: now,
			Thumbnail: req.Thumbnail, ChannelName: req.ChannelName}
		if prev != nil && prev.NowPlaying != nil {
			np.VideoURL, np.Title = prev.NowPlaying.VideoURL, prev.NowPlaying.Title
		}
		if req.VideoURL != nil {
			np.VideoURL = *req.VideoURL
		}
		if req.Title != nil {
			np.Title = *req.Title
		}
		if np.VideoURL != "" {
			rec.NowPlaying = &np
			a.presence[user] = np
		}
		rec.LastPlaybackAt = &now
	case "stop":
		rec.LastPlaybackAt = &now
		delete(a.presence, user)
	}
	a.sessions[id] = rec
}
