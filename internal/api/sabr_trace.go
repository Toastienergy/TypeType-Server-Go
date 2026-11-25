package api

import (
	"net/http"
	"strings"
	"time"

	"typetype.dev/server/internal/sabr"
)

// startupLogSpan is how long after its creation every request of a session is logged; later ones are logged only
// when slow or not ready, so a whole playback does not flood the log.
const (
	startupLogSpan = 20 * time.Second
	slowSabrCall   = 100 * time.Millisecond
)

// sabrTrace logs one step of a playback start with what joins it to the rest: the video, the session and its age,
// and the web player's trace id when it sends one.
func (s *Server) sabrTrace(r *http.Request, msg string, ss *sabr.Session, kv ...any) {
	attrs := make([]any, 0, len(kv)+8)
	if ss != nil {
		id := ss.ID
		if len(id) > 8 {
			id = id[:8]
		}
		attrs = append(attrs, "videoId", ss.VideoID, "session", id, "ageMs", ss.Age().Milliseconds())
	}
	if t := r.Header.Get("X-Playback-Trace-ID"); t != "" {
		attrs = append(attrs, "trace", t)
	}
	s.Log.Info(msg, append(attrs, kv...)...)
}

// startupStep tells whether a session request is worth a log line: during the start, or when it waited or failed.
func startupStep(ss *sabr.Session, took time.Duration, ok bool) bool {
	return ss.Age() < startupLogSpan || took >= slowSabrCall || !ok
}

// userKind is who asks, without naming them.
func userKind(user string) string {
	switch {
	case user == "":
		return "anonymous"
	case strings.HasPrefix(user, "guest:"):
		return "guest"
	}
	return "account"
}

// prewarmMiss says how the prewarmed sessions of this user's video differ from the one asked for.
func prewarmMiss(want string, have []string) string {
	if len(have) == 0 {
		return "none_prepared"
	}
	names := []string{"user", "video", "videoItag", "audioItag", "audioTrack", "startMs", "audioOnly"}
	w := strings.Split(want, "|")
	var out []string
	for _, k := range have {
		h := strings.Split(k, "|")
		var diff []string
		for i := range min(len(w), len(h), len(names)) {
			if w[i] != h[i] {
				diff = append(diff, names[i]+":"+h[i]+"->"+w[i])
			}
		}
		out = append(out, strings.Join(diff, ","))
	}
	return strings.Join(out, ";")
}
