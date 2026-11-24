package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/sabr"
)

var voiceVideoRe = regexp.MustCompile(`(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})`)

const voiceMaxBytes = 4 << 20

// voiceFlights shares one download between the viewers asking for the same voice reply at once.
var voiceFlights sync.Map

type voiceFlight struct {
	done chan struct{}
	data []byte
}

func isYouTubeHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "youtu.be" || h == "youtube.com" || strings.HasSuffix(h, ".youtube.com")
}

// commentVoice is /comments/voice, CommentVoiceAudioService: a YouTube voice reply is an audio-only video of at most
// two minutes; its AAC track is downloaded whole through SABR, at most 4 MiB, and kept a day in Dragonfly.
func (s *Server) commentVoice(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.publicAccess(w, r); !ok {
		return
	}
	q := r.URL.Query()
	if !q.Has("url") {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	raw := q.Get("url")
	m := voiceVideoRe.FindStringSubmatch(raw)
	if !isYouTubeHost(raw) || m == nil {
		plain(w, r, http.StatusBadRequest, "Not a YouTube voice reply")
		return
	}
	data := s.voiceAudio(r.Context(), m[1])
	if data == nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Voice reply audio is not available", "no_playable_streams")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Type", "audio/mp4")
	w.Write(data)
}

func (s *Server) voiceAudio(ctx context.Context, videoID string) []byte {
	f := &voiceFlight{done: make(chan struct{})}
	if cur, loaded := voiceFlights.LoadOrStore(videoID, f); loaded {
		other := cur.(*voiceFlight)
		select {
		case <-other.done:
			return other.data
		case <-ctx.Done():
			return nil
		}
	}
	defer func() {
		voiceFlights.Delete(videoID)
		close(f.done)
	}()
	f.data = s.loadVoice(context.WithoutCancel(ctx), videoID)
	return f.data
}

func (s *Server) loadVoice(ctx context.Context, videoID string) []byte {
	key := "comment-voice:v1:" + videoID
	if v, ok, _ := s.Cache.Get(ctx, key); ok {
		if b, err := base64.StdEncoding.DecodeString(v); err == nil {
			return b
		}
	}
	ictx, cancel := context.WithTimeout(ctx, sabrInfoTimeout)
	info, err := s.Sabr.Token().Info(ictx, videoID)
	cancel()
	if err != nil {
		return nil
	}
	var audio *sabr.Format
	for i := range info.Formats {
		f := &info.Formats[i]
		if f.IsVideo() {
			return nil
		}
		if f.IsAudio() && strings.HasPrefix(f.MimeType, "audio/mp4") && (audio == nil || f.Bitrate > audio.Bitrate) {
			audio = f
		}
	}
	if audio == nil || audio.DurationMs.Int() < 1 || audio.DurationMs.Int() > 120_000 {
		return nil
	}
	ss := s.Sabr.Create(info, videoID, "comment-voice", audio, audio, 0, true, 0)
	defer s.Sabr.Release(ss)
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var out []byte
	emit := func(kind byte, _, _ int, data []byte) error {
		if len(out)+len(data) > voiceMaxBytes {
			return errVoiceTooLarge
		}
		out = append(out, data...)
		return nil
	}
	if err := downloadSabr(dctx, ss, []*sabr.Format{audio}, 0, 1, emit); err != nil || len(out) == 0 {
		return nil
	}
	s.Cache.Set(ctx, key, base64.StdEncoding.EncodeToString(out), 24*time.Hour)
	return out
}

var errVoiceTooLarge = errors.New("voice reply is larger than 4 MiB")
