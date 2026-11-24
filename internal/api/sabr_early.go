package api

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/sabr"
)

// earlySession starts the playback session the web player is about to ask for, as soon as the page asks for the
// bootstrap: the player only creates it after loading its code, a few hundred milliseconds later, and finds it
// prewarmed under the same key. The formats are the ones the web player picks by default (defaultSabrItag and
// pickSabrAudio): the highest height not above the viewer's default quality, H.264 first, then VP9, then AV1, and
// the default audio. A guess the player does not confirm costs the initialization segments and a few seconds of
// media, and the idle sweep releases it.
func (s *Server) earlySession(r *http.Request, id string, in *sabr.Info) {
	if in == nil || in.Live || strings.EqualFold(os.Getenv("SABR_EARLY_SESSION"), "off") {
		return
	}
	user := s.optionalUserID(r)
	limit := 1080
	if user != "" && !strings.HasPrefix(user, "guest:") {
		ctx, cancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
		var q string
		if s.DB.QueryRow(ctx, "SELECT default_quality FROM settings WHERE user_id = $1", user).Scan(&q) == nil {
			if h, err := strconv.Atoi(strings.TrimSuffix(strings.SplitN(q, "p", 2)[0], " ")); err == nil && h > 0 {
				limit = h
			}
		}
		cancel()
	}
	video := defaultWebVideo(in, limit)
	audio := in.SelectAudio(0, "")
	if video == nil || audio == nil {
		return
	}
	var start int64
	if t := r.URL.Query().Get("t"); t != "" {
		if n, err := strconv.ParseInt(strings.TrimSuffix(t, "s"), 10, 64); err == nil && n > 0 {
			start = n * 1000
		}
	}
	vi, ai := video.Itag, audio.Itag
	p := playbackRequest{VideoItag: &vi, AudioItag: &ai, StartTimeMs: &start}
	owner := user
	if owner == "" {
		owner = id
	}
	key := prewarmKey(user, id, p, start)
	if s.Sabr.HasPrewarmed(key) {
		return
	}
	s.Sabr.Remember(key, s.Sabr.Create(in, id, owner, video, audio, start, false, 0), start)
}

func webCodecRank(mime string) int {
	switch {
	case strings.Contains(mime, "avc1"):
		return 0
	case strings.Contains(mime, "vp9") || strings.Contains(mime, "vp09"):
		return 1
	case strings.Contains(mime, "av01"):
		return 2
	}
	return 3
}

func defaultWebVideo(in *sabr.Info, limit int) *sabr.Format {
	tier := func(f *sabr.Format) int {
		if n, err := strconv.Atoi(strings.SplitN(f.QualityLabel, "p", 2)[0]); err == nil && n > 0 {
			return n
		}
		return f.Height
	}
	var best *sabr.Format
	for i := range in.Formats {
		f := &in.Formats[i]
		if !f.IsVideo() || in.SelectVideo(f.Itag) == nil || webCodecRank(f.MimeType) > 2 || tier(f) > limit {
			continue
		}
		switch {
		case best == nil, tier(f) > tier(best):
			best = f
		case tier(f) == tier(best) && webCodecRank(f.MimeType) < webCodecRank(best.MimeType):
			best = f
		case tier(f) == tier(best) && webCodecRank(f.MimeType) == webCodecRank(best.MimeType) && f.Bitrate > best.Bitrate:
			best = f
		}
	}
	return best
}
