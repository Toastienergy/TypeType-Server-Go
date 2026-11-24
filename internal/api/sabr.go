package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/sabr"
)

const (
	sabrRetryAfterMs = 250
	windowWait       = 1500 * time.Millisecond
	initWait         = 6 * time.Second
	segmentWait      = 4 * time.Second
	sabrInfoTimeout  = 8 * time.Second
)

func (s *Server) sabrRoutes(rt *Router) {
	rt.Handle("POST", "/sabr/playback/{videoId}", s.sabrCreate)
	rt.Handle("POST", "/sabr/playback/{sessionId}/seek", s.sabrSeek)
	rt.Handle("POST", "/sabr/playback/{sessionId}/window", s.sabrWindow("window"))
	rt.Handle("POST", "/sabr/playback/{sessionId}/prefetch", s.sabrWindow("prefetch"))
	rt.Handle("POST", "/sabr/playback/{sessionId}/segments", s.sabrWindow("segments"))
	rt.Handle("POST", "/sabr/playback/{sessionId}/position", s.sabrPosition)
	rt.Handle("DELETE", "/sabr/playback/{sessionId}", s.sabrDelete)
	rt.Handle("GET", "/sabr/playback/{sessionId}/state", s.sabrState)
	rt.Handle("GET", "/sabr/playback/{sessionId}/{itag}/init", s.sabrMedia(true))
	rt.Handle("GET", "/sabr/playback/{sessionId}/{itag}/segment/{seq}", s.sabrMedia(false))
}

type playbackRequest struct {
	VideoItag    *int    `json:"videoItag"`
	AudioItag    *int    `json:"audioItag"`
	AudioTrackID *string `json:"audioTrackId"`
	StartTimeMs  *int64  `json:"startTimeMs"`
	PlayerTimeMs *int64  `json:"playerTimeMs"`
	AudioOnly    *bool   `json:"audioOnly"`
	IsLive       *bool   `json:"isLive"`
}

// readPlaybackRequest takes the JSON body when there is one and falls back to the query, field by field.
func readPlaybackRequest(r *http.Request) playbackRequest {
	var p playbackRequest
	if body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16)); err == nil && len(body) > 0 {
		json.Unmarshal(body, &p)
	}
	q := r.URL.Query()
	intQ := func(dst **int, key string) {
		if *dst == nil {
			if v, err := strconv.Atoi(q.Get(key)); err == nil {
				*dst = &v
			}
		}
	}
	longQ := func(dst **int64, key string) {
		if *dst == nil {
			if v, err := strconv.ParseInt(q.Get(key), 10, 64); err == nil {
				*dst = &v
			}
		}
	}
	boolQ := func(dst **bool, key string) {
		if *dst == nil {
			switch q.Get(key) {
			case "true":
				t := true
				*dst = &t
			case "false":
				f := false
				*dst = &f
			}
		}
	}
	intQ(&p.VideoItag, "videoItag")
	intQ(&p.AudioItag, "audioItag")
	if p.AudioTrackID == nil && q.Has("audioTrackId") {
		v := q.Get("audioTrackId")
		p.AudioTrackID = &v
	}
	longQ(&p.StartTimeMs, "startTimeMs")
	longQ(&p.PlayerTimeMs, "playerTimeMs")
	boolQ(&p.AudioOnly, "audioOnly")
	boolQ(&p.IsLive, "isLive")
	return p
}

func (p playbackRequest) start() int64 {
	switch {
	case p.PlayerTimeMs != nil:
		return max(0, *p.PlayerTimeMs)
	case p.StartTimeMs != nil:
		return max(0, *p.StartTimeMs)
	}
	return 0
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

type playbackResponse struct {
	SessionID    string  `json:"sessionId"`
	VideoID      string  `json:"videoId"`
	ManifestURL  *string `json:"manifestUrl"`
	VideoItag    int     `json:"videoItag"`
	AudioItag    int     `json:"audioItag"`
	AudioTrackID *string `json:"audioTrackId"`
	StartTimeMs  int64   `json:"startTimeMs"`
	Generation   int64   `json:"generation"`
	Ready        bool    `json:"ready"`
	Status       string  `json:"status"`
	RetryAfterMs *int64  `json:"retryAfterMs"`
	Live         any     `json:"live"`
}

func (s *Server) respondPrepared(w http.ResponseWriter, r *http.Request, ss *sabr.Session, start int64, ready bool) {
	if kind, msg := ss.Failure(); kind != "" {
		s.Log.Warn("sabr session failed", "videoId", ss.VideoID, "kind", kind, "msg", msg)
		s.Sabr.Release(ss)
		sabrFailure(w, r, kind)
		return
	}
	resp := playbackResponse{SessionID: ss.ID, VideoID: ss.VideoID, VideoItag: ss.Video.Itag, AudioItag: ss.Audio.Itag,
		AudioTrackID: ss.Audio.AudioTrackID(), StartTimeMs: start, Generation: ss.Generation(), Ready: ready, Status: "preparing"}
	status := http.StatusAccepted
	if ready {
		m := "/sabr/playback/" + ss.ID + "/manifest"
		resp.ManifestURL, resp.Status, status = &m, "ready", http.StatusOK
	} else {
		retry := int64(sabrRetryAfterMs)
		resp.RetryAfterMs = &retry
	}
	httpx.JSON(w, status, resp)
}

// sabrFailure is SabrFailureResponse: 503 with Retry-After for what may pass, 422 when the video cannot play.
func sabrFailure(w http.ResponseWriter, r *http.Request, kind string) {
	type f struct {
		status int
		msg    string
		code   string
		retry  string
	}
	m := map[string]f{
		sabr.FailUpstreamRefused: {503, "YouTube refused this playback session", "sabr_upstream_refused", "30"},
		sabr.FailSessionChanged:  {503, "The YouTube session changed, create a new playback session", "sabr_session_expired", "2"},
		sabr.FailUpstreamTimeout: {503, "YouTube did not answer in time", "sabr_upstream_timeout", "5"},
		sabr.FailContentMissing:  {422, "SABR playback is not available for this video", "sabr_content_unavailable", ""},
	}
	v, ok := m[kind]
	if !ok {
		v = f{503, "SABR playback session failed", "sabr_session_failed", "5"}
	}
	if v.retry != "" {
		w.Header().Set("Retry-After", v.retry)
	}
	httpx.Error(w, r, v.status, v.msg, v.code)
}

func prewarmKey(user, video string, p playbackRequest, start int64) string {
	return strings.Join([]string{user, video, strconv.Itoa(deref(p.VideoItag)), strconv.Itoa(deref(p.AudioItag)),
		deref(p.AudioTrackID), strconv.FormatInt(start, 10), strconv.FormatBool(deref(p.AudioOnly))}, "|")
}

func (s *Server) sabrCreate(w http.ResponseWriter, r *http.Request) {
	videoID := r.PathValue("videoId")
	user := s.optionalUserID(r)
	p := readPlaybackRequest(r)
	start := p.start()
	key := prewarmKey(user, videoID, p, start)
	isPrewarm := r.URL.Query().Get("prewarm") == "true"
	if !isPrewarm {
		if ss, at := s.Sabr.TakePrewarmed(key); ss != nil {
			if kind, _ := ss.Failure(); kind == "" {
				s.respondPrepared(w, r, ss, at, s.awaitReady(r.Context(), ss, at, deref(p.AudioOnly)))
				return
			}
		}
	}
	began := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), sabrInfoTimeout)
	info, err := s.Sabr.Token().Info(ctx, videoID)
	cancel()
	if needsSignedIn(videoID, err) {
		if s.hasYoutubeSession(r.Context(), user) {
			info, _, err = s.signedInSabr(r.Context(), user, videoID)
		}
	}
	infoAt := time.Since(began)
	if err != nil {
		s.Log.Warn("sabr info", "videoId", videoID, "err", err)
		plain(w, r, http.StatusUnprocessableEntity, "SABR probe failed")
		return
	}
	audio := info.SelectAudio(deref(p.AudioItag), deref(p.AudioTrackID))
	if audio == nil {
		plain(w, r, http.StatusUnprocessableEntity, "No SABR audio for this video")
		return
	}
	video := info.SelectVideo(deref(p.VideoItag))
	if video == nil {
		plain(w, r, http.StatusUnprocessableEntity, "No SABR video for this video")
		return
	}
	owner := user
	if owner == "" {
		owner = videoID
	}
	ss := s.Sabr.Create(info, videoID, owner, video, audio, start, deref(p.AudioOnly), 0)
	if s.acceptance != nil {
		s.acceptance.played(videoID)
	}
	s.awaitInit(r.Context(), ss)
	initAt := time.Since(began)
	if isPrewarm {
		s.Sabr.Remember(key, ss, start)
	}
	ready := s.awaitReady(r.Context(), ss, start, deref(p.AudioOnly))
	s.Log.Info("sabr_create", "videoId", videoID, "prewarm", isPrewarm, "infoMs", infoAt.Milliseconds(), "initMs", initAt.Milliseconds(),
		"readyMs", time.Since(began).Milliseconds(), "ready", ready, "requests", ss.Requests())
	s.respondPrepared(w, r, ss, start, ready)
}

// awaitInit waits for the initialization segments, like the Kotlin preload: without them nothing can play, so
// the session fails and the create answers the failure.
func (s *Server) awaitInit(ctx context.Context, ss *sabr.Session) {
	for _, f := range []*sabr.Format{ss.Audio, ss.Video} {
		if f == ss.Video && ss.AudioOnly {
			continue
		}
		if _, ok := ss.Init(ctx, f.Itag, initWait); !ok && ctx.Err() == nil {
			if kind, _ := ss.Failure(); kind == "" {
				ss.Fail(sabr.FailUpstreamTimeout, "SABR initialization unavailable for itag "+strconv.Itoa(f.Itag))
			}
			return
		}
	}
}

// awaitReady waits a little for the first second to play from start, so most creates and seeks answer ready.
func (s *Server) awaitReady(ctx context.Context, ss *sabr.Session, start int64, audioOnly bool) bool {
	req := sabr.DefaultWindowRequest()
	req.PlayerTimeMs, req.AudioOnly, req.Generation = start, audioOnly, ss.Generation()
	deadline := time.Now().Add(windowWait)
	for {
		if ss.BuildWindow(&req).Ready {
			return true
		}
		left := time.Until(deadline)
		if left <= 0 || ctx.Err() != nil {
			return false
		}
		if kind, _ := ss.Failure(); kind != "" {
			return false
		}
		ss.Wait(ctx, left)
	}
}

func (s *Server) sabrSession(w http.ResponseWriter, r *http.Request) *sabr.Session {
	ss := s.Sabr.Lookup(r.PathValue("sessionId"))
	if ss == nil {
		plain(w, r, http.StatusNotFound, "No active SABR playback session")
	}
	return ss
}

func (s *Server) sabrSeek(w http.ResponseWriter, r *http.Request) {
	ss := s.sabrSession(w, r)
	if ss == nil {
		return
	}
	p := readPlaybackRequest(r)
	target := p.start()
	keeps := (p.VideoItag == nil || *p.VideoItag == ss.Video.Itag) && (p.AudioItag == nil || *p.AudioItag == ss.Audio.Itag) &&
		(deref(p.AudioTrackID) == "" || ss.Audio.AudioTrackID() != nil && *ss.Audio.AudioTrackID() == *p.AudioTrackID)
	if keeps {
		ss.SeekTo(target, deref(p.AudioOnly))
		s.respondPrepared(w, r, ss, target, s.awaitReady(r.Context(), ss, target, deref(p.AudioOnly)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sabrInfoTimeout)
	info, err := s.Sabr.Token().Info(ctx, ss.VideoID)
	cancel()
	if err != nil {
		plain(w, r, http.StatusUnprocessableEntity, "SABR probe failed")
		return
	}
	audioItag, track := ss.Audio.Itag, deref(ss.Audio.AudioTrackID())
	if p.AudioItag != nil {
		audioItag = *p.AudioItag
	}
	if p.AudioTrackID != nil {
		track = *p.AudioTrackID
	}
	audio := info.SelectAudio(audioItag, track)
	if audio == nil {
		plain(w, r, http.StatusUnprocessableEntity, "No SABR audio for this video")
		return
	}
	videoItag := ss.Video.Itag
	if p.VideoItag != nil {
		videoItag = *p.VideoItag
	}
	video := info.SelectVideo(videoItag)
	if video == nil {
		plain(w, r, http.StatusUnprocessableEntity, "No SABR video for this video")
		return
	}
	next := s.Sabr.Create(info, ss.VideoID, ss.UserID, video, audio, target, deref(p.AudioOnly), ss.Generation()+1)
	s.Sabr.Release(ss)
	s.awaitInit(r.Context(), next)
	s.respondPrepared(w, r, next, target, s.awaitReady(r.Context(), next, target, deref(p.AudioOnly)))
}

func (s *Server) sabrDelete(w http.ResponseWriter, r *http.Request) {
	if ss := s.Sabr.Lookup(r.PathValue("sessionId")); ss != nil {
		s.Sabr.Release(ss)
	}
	w.WriteHeader(http.StatusNoContent)
}
