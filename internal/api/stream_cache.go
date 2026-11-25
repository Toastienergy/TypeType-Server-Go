package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"typetype.dev/server/internal/sabr"
)

// youtubeStreamCore is the part of a YouTube video page that is the same for every viewer, kept as CachedStreamService
// keeps it ("stream:v6:<url>", streamCacheTTL); the viewer's filters run on a copy afterwards.
func (s *Server) youtubeStreamCore(ctx context.Context, id, raw string) (*streamResponse, *extractFail) {
	area := cacheArea[*streamResponse]{name: "stream", noBackoff: true, admit: func(r *streamResponse) admission {
		return valid(streamCacheTTL(r, time.Now().Unix()))
	}}
	return publicCached(s, ctx, area, "stream:v6:"+raw, func(ctx context.Context) (*streamResponse, *extractFail) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		infoc := make(chan *sabr.Info, 1)
		var tokenDown bool
		go func() {
			in, err := s.Sabr.Token().Info(ctx, id)
			if err != nil {
				s.Log.Warn("stream sabr info", "videoId", id, "err", err)
				var ne net.Error
				tokenDown = errors.As(err, &ne) || errors.Is(err, syscall.ECONNREFUSED)
			}
			infoc <- in
		}()
		sbc := make(chan sponsorResult, 1)
		go func() { sbc <- s.sponsor.segments(ctx, id) }()
		watch, err := s.YT.FetchWatch(ctx, id)
		s.rec.streamOpened(ctx, refusedByYoutube(err) || watch != nil && watch.Restriction != nil && watch.Restriction.Code == "provider_access_blocked")
		in := <-infoc
		if err != nil && in == nil {
			s.Log.Warn("stream watch", "videoId", id, "err", err)
			return nil, &extractFail{Status: http.StatusBadGateway, Message: "Could not extract this video", Code: "extraction_failed", bad: true}
		}
		resp := buildStream(id, raw, watch, in)
		if watch != nil {
			if watch.Related != nil {
				resp.RelatedStreams = watch.Related
			}
			resp.restriction = watch.Restriction
		}
		sb := <-sbc
		resp.SponsorBlockSegments, resp.SponsorBlockAvailable = sb.segments, sb.available
		resp.tokenDown = tokenDown
		return &resp, nil
	})
}

var mediaExpiry = regexp.MustCompile(`(?i)(?:^|[?&#=])(?:deadline|expire|expires|exp|expiry|expire_at|expires_at)=([0-9]+)`)

// streamCacheTTL is streamCacheTtlSeconds: no cache for lives or a page without media; otherwise six hours (five
// minutes without dislikes), capped by the earliest signed media expiry less five minutes, five minutes for a
// finished live and two while SponsorBlock had no answer.
func streamCacheTTL(r *streamResponse, now int64) int64 {
	if r.IsLive || r.IsPostLive || strings.HasPrefix(r.HlsURL, "/streams/hls-manifest?token=") ||
		len(r.VideoStreams)+len(r.VideoOnlyStreams)+len(r.AudioStreams) == 0 && r.HlsURL == "" {
		return 0
	}
	urls := []string{r.HlsURL, r.DashMpdURL}
	for _, v := range append(r.VideoStreams, r.VideoOnlyStreams...) {
		urls = append(urls, v.URL, deref(v.ManifestURL), deref(v.SabrSessionURL))
	}
	for _, a := range r.AudioStreams {
		urls = append(urls, a.URL, deref(a.ManifestURL), deref(a.SabrSessionURL))
	}
	signed, deadline := false, int64(-1)
	for _, u := range urls {
		if p, err := url.Parse(u); err == nil && (strings.Contains(p.Hostname(), "bilivideo") || strings.Contains(p.Hostname(), "akamaized") ||
			strings.Contains(p.Hostname(), "nicovideo") || strings.Contains(p.Hostname(), "nimg.jp")) {
			signed = true
		}
		d := u
		for range 3 {
			if n, err := url.QueryUnescape(d); err == nil {
				d = n
			}
		}
		for _, m := range mediaExpiry.FindAllStringSubmatch(d, -1) {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && (deadline < 0 || n < deadline) {
				deadline = n
			}
		}
	}
	ttl := int64(21600)
	if r.DislikeCount < 0 && !signed {
		ttl = 300
	}
	if signed {
		ttl = min(ttl, 3600)
	}
	if deadline >= 0 {
		left := deadline - now - 300
		if left < 60 {
			return 0
		}
		ttl = min(ttl, left)
	}
	if r.IsLiveContent {
		ttl = min(ttl, 300)
	}
	if !r.SponsorBlockAvailable {
		ttl = min(ttl, 120)
	}
	return ttl
}
