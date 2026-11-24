package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

type subtitleResult struct {
	content []byte
	live    bool
	outcome fetchOutcome
}

func (st *subtitles) resolve(ctx context.Context, sel subtitleSel) (tracks []string, live bool, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	invc := make(chan []youtube.Subtitle, 1)
	go func() { invc <- st.inventory(ctx, sel.videoID) }()
	w, err := st.s.YT.FetchWatch(ctx, sel.videoID)
	inv := <-invc
	var own []youtube.Subtitle
	if err == nil && w != nil {
		own, live = w.Subtitles, w.Live || w.LiveContent
	}
	if _, signedIn := requiresSession.Load(sel.videoID); signedIn && len(inv) == 0 && len(own) == 0 && st.s.hasYoutubeSession(ctx, sel.viewer) {
		if _, player, err := st.s.signedInSabr(ctx, sel.viewer, sel.videoID); err == nil {
			own = youtube.ParsePlayer(sel.videoID, player).Subtitles
		}
	}
	if len(inv) == 0 && own == nil {
		return nil, false, false
	}
	seen := map[string]bool{}
	for _, list := range [][]youtube.Subtitle{inv, own} {
		if u := trackURL(list, sel); u != "" && !seen[u] {
			seen[u] = true
			tracks = append(tracks, u)
		}
	}
	return tracks, live, true
}

func (st *subtitles) load(ctx context.Context, sel subtitleSel) subtitleResult {
	for attempt := range 2 {
		tracks, live, ok := st.resolve(ctx, sel)
		if !ok {
			return subtitleResult{outcome: subUnavailable}
		}
		if len(tracks) == 0 {
			return subtitleResult{outcome: subNotFound}
		}
		b, out := st.fetchTrack(ctx, tracks[0], sel.format)
		if out != subReady && out != subThrottled && len(tracks) > 1 {
			if b2, out2 := st.fetchTrack(ctx, tracks[1], sel.format); out2 == subReady {
				b, out = b2, out2
			}
		}
		if out == subExpired && attempt == 0 {
			continue
		}
		return subtitleResult{content: b, live: live, outcome: out}
	}
	return subtitleResult{outcome: subExpired}
}

func (st *subtitles) get(ctx context.Context, sel subtitleSel) subtitleResult {
	if e, ok := st.cached(ctx, sel); ok {
		return subtitleResult{content: []byte(e.Content), live: e.IsLive}
	}
	k := sel.key()
	st.mu.Lock()
	if ch, ok := st.inflight[k]; ok {
		st.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return subtitleResult{outcome: subUnavailable}
		}
		if e, ok := st.cached(ctx, sel); ok {
			return subtitleResult{content: []byte(e.Content), live: e.IsLive}
		}
		return subtitleResult{outcome: subUnavailable}
	}
	ch := make(chan struct{})
	st.inflight[k] = ch
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		delete(st.inflight, k)
		st.mu.Unlock()
		close(ch)
	}()
	select {
	case st.permits <- struct{}{}:
		defer func() { <-st.permits }()
	default:
		return subtitleResult{outcome: subUnavailable}
	}
	res := st.load(ctx, sel)
	if res.outcome == subReady {
		e := subtitleEntry{Content: string(res.content), IsLive: res.live, at: time.Now()}
		st.remember(k, e)
		b, _ := json.Marshal(e)
		ttl := 6 * time.Hour
		if res.live {
			ttl = 5 * time.Second
		}
		st.s.Cache.Set(ctx, k, string(b), ttl)
	}
	return res
}

func (s *Server) subtitleRoutes(rt *Router) {
	rt.Handle("GET", "/subtitles/youtube/{videoId}", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		format := q.Get("format")
		if !q.Has("format") {
			format = "vtt"
		}
		sel := subtitleSel{videoID: strings.TrimSpace(r.PathValue("videoId")), language: strings.TrimSpace(q.Get("language")),
			variant: q.Get("variant"), format: format, source: strings.TrimSpace(q.Get("sourceLanguage")),
			translation: strings.TrimSpace(q.Get("translation")), name: strings.TrimSpace(q.Get("name")), viewer: s.optionalUserID(r)}
		if !videoIDRe.MatchString(sel.videoID) || !validTag(sel.language) || sel.variant != "manual" && sel.variant != "auto" ||
			sel.format != "vtt" && sel.format != "ttml" || sel.source != "" && !validTag(sel.source) ||
			sel.translation != "" && !validTag(sel.translation) || len([]rune(sel.name)) > 128 {
			httpx.Error(w, r, http.StatusBadRequest, "Invalid YouTube subtitle request", "subtitle_request_invalid")
			return
		}
		res := s.subs.get(r.Context(), sel)
		switch res.outcome {
		case subReady:
			cc := "public, max-age=21600, stale-while-revalidate=3600"
			if res.live {
				cc = "no-store"
			}
			w.Header().Set("Cache-Control", cc)
			w.Header().Set("Content-Type", map[string]string{"vtt": "text/vtt; charset=utf-8", "ttml": "application/ttml+xml; charset=utf-8"}[sel.format])
			w.Write(res.content)
		case subNotFound:
			httpx.Error(w, r, http.StatusNotFound, "Subtitle track not found", "subtitle_track_not_found")
		case subThrottled:
			httpx.Error(w, r, http.StatusTooManyRequests, "YouTube temporarily throttled subtitle retrieval", "subtitle_upstream_throttled")
		case subExpired:
			httpx.Error(w, r, http.StatusBadGateway, "YouTube subtitle URL expired after refresh", "subtitle_url_expired")
		case subInvalid:
			httpx.Error(w, r, http.StatusBadGateway, "YouTube returned invalid subtitle content", "subtitle_payload_invalid")
		default:
			httpx.Error(w, r, http.StatusBadGateway, "YouTube subtitle retrieval failed", "subtitle_upstream_unavailable")
		}
	})
}
