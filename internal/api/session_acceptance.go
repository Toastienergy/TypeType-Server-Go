package api

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"typetype.dev/server/internal/sabr"
)

// acceptance is TokenSessionAcceptance with TokenSessionWatch: YouTube rates the exit when a token session is
// minted and the verdict sticks to the session. A session on hold is checked, replaced until one is accepted, and
// only then is the egress moved; every new session is checked with the last video played before a viewer meets it.
type acceptance struct {
	s          *Server
	rotation   *egressRotation
	tries      int
	running    atomic.Bool
	finishedAt atomic.Int64
	mu         sync.Mutex
	lastVideo  string
	checked    string
}

const acceptanceRest = 2 * time.Minute

func (s *Server) startAcceptance() {
	a := &acceptance{s: s, rotation: newEgressRotation(s), tries: 6}
	if v := os.Getenv("YOUTUBE_SESSION_REPLACEMENTS"); v != "" {
		if n, ok := atoiBounded(v, 0, 20); ok {
			a.tries = n
		}
	}
	s.acceptance = a
	s.Sabr.SetOnHold(a.onHold)
	go func() {
		for range time.Tick(time.Minute) {
			a.watchOnce()
		}
	}()
}

func atoiBounded(v string, lo, hi int) (int, bool) {
	n := 0
	for _, c := range strings.TrimSpace(v) {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return min(max(n, lo), hi), true
}

func (a *acceptance) played(videoID string) {
	a.mu.Lock()
	a.lastVideo = videoID
	a.mu.Unlock()
}

// onHold starts one recovery; signals during one or within two minutes after it are dropped. It checks with the
// last video played, not the one that raised the signal, which may be unplayable for its own reasons.
func (a *acceptance) onHold(reason, videoID string) {
	if time.Since(time.UnixMilli(a.finishedAt.Load())) < acceptanceRest || !a.running.CompareAndSwap(false, true) {
		return
	}
	a.mu.Lock()
	if a.lastVideo != "" {
		videoID = a.lastVideo
	}
	a.mu.Unlock()
	go func() {
		defer func() {
			a.finishedAt.Store(time.Now().UnixMilli())
			a.running.Store(false)
		}()
		a.recover(reason, videoID)
	}()
}

func (a *acceptance) recover(reason, videoID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if a.accepts(ctx, videoID) {
		a.s.Log.Info("youtube_session kept", "reason", reason)
		return
	}
	for attempt := 1; attempt <= a.tries; attempt++ {
		a.s.Sabr.Token().Renew(ctx, videoID)
		ok := a.accepts(ctx, videoID)
		a.s.Log.Warn("youtube_session replaced", "reason", reason, "attempt", attempt, "accepted", ok)
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
		}
	}
	if a.rotation != nil {
		a.s.Log.Warn("youtube_session exit asked", "reason", reason, "accepted", a.rotation.rotate(ctx, reason, videoID))
	}
}

// accepts is SabrEgressExitCheck: a session opened two minutes into the video, with the lightest H.264 video, is
// accepted (protection 1 with a request answered) or not (on hold, refused, or nothing within 10 s). YouTube
// serves the start of a video even to a session on hold, so only a start away from zero tells.
func (a *acceptance) accepts(ctx context.Context, videoID string) bool {
	info, err := a.s.Sabr.Token().Info(ctx, videoID)
	if err != nil {
		return false
	}
	audio := info.SelectAudio(0, "")
	var video *sabr.Format
	for i := range info.Formats {
		f := &info.Formats[i]
		if f.IsVideo() && strings.Contains(f.MimeType, "avc1") && (video == nil || f.Height < video.Height ||
			f.Height == video.Height && f.Bitrate < video.Bitrate) {
			video = f
		}
	}
	if audio == nil || video == nil {
		return false
	}
	ss := a.s.Sabr.Create(info, videoID, sabr.ProbeUser, video, audio, 120_000, false, 0)
	defer a.s.Sabr.Release(ss)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if kind, _ := ss.Failure(); kind != "" {
			return false
		}
		switch p := ss.Protection(); {
		case p >= 2:
			return false
		case p == 1 && ss.Requests() > 0:
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// watchOnce checks a token session the first time it is seen, with the last video played.
func (a *acceptance) watchOnce() {
	a.mu.Lock()
	video, checked := a.lastVideo, a.checked
	a.mu.Unlock()
	if video == "" || a.running.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g, err := a.s.Sabr.Token().Generation(ctx, video, false)
	if err != nil || g == "" || g == checked {
		return
	}
	a.mu.Lock()
	a.checked = g
	a.mu.Unlock()
	ok := a.accepts(ctx, video)
	a.s.Log.Info("youtube_session checked", "generation", g, "accepted", ok)
	if !ok {
		a.onHold("session_check", video)
	}
}
