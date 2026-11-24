package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
)

// Provider media handles are ProviderMediaHandleService's: /media/m1_<id> stands for a Bilibili or NicoNico media URL
// and its mirrors, stored under the Kotlin key and JSON shape so a handle made by either server plays on the other.
type mediaTarget struct {
	URL        string   `json:"url"`
	DomandBid  *string  `json:"domandBid,omitempty"`
	Alternates []string `json:"alternateUrls,omitempty"`
}

var (
	mediaHandleRe = regexp.MustCompile(`^m1_[A-Za-z0-9_-]{24}$`)
	mediaExpiryRe = regexp.MustCompile(`(?i)(?:^|[?&#=])(?:deadline|Expires|exp)=(\d+)`)
)

func mediaTTL(raw string) time.Duration {
	dec, err := url.QueryUnescape(raw)
	if err != nil {
		dec = raw
	}
	var min int64 = -1
	for _, m := range mediaExpiryRe.FindAllStringSubmatch(dec, -1) {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && (min < 0 || n < min) {
			min = n
		}
	}
	if min < 0 {
		return 30 * time.Minute
	}
	ttl := time.Duration(min-time.Now().Unix()-30) * time.Second
	if ttl <= 0 {
		return 0
	}
	return max(time.Second, min64(ttl, time.Hour))
}

func min64(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// mediaPath stores the target and returns its /media path, or "" when the URL cannot be proxied or has expired.
func (s *Server) mediaPath(ctx context.Context, raw string, alternates []string, bid *string) string {
	if bid == nil || strings.TrimSpace(*bid) == "" {
		bid = nicoBid(nil, raw)
	}
	raw, _, _ = strings.Cut(raw, "#")
	u, msg := validateProxyURL(raw)
	provider := ""
	if msg == "" {
		provider = proxyProvider(u.Hostname())
	}
	if provider != "bilibili" && provider != "niconico" {
		return ""
	}
	clean := trackingParams.ReplaceAllString(u.String(), "")
	ttl := mediaTTL(clean)
	var alts []string
	if provider == "bilibili" {
		blocked := akamaiBlacklisted()
		var candidates []string
		for _, a := range alternates {
			if !blocked || !isAkamaiURL(a) {
				candidates = append(candidates, a)
			}
		}
		for _, a := range append(candidates, uposMirrors(clean, blocked, bilibiliRegion())...) {
			au, msg := validateProxyURL(a)
			if msg != "" || proxyProvider(au.Hostname()) != "bilibili" || au.EscapedPath() != u.EscapedPath() || len(alts) >= 5 {
				continue
			}
			a = trackingParams.ReplaceAllString(au.String(), "")
			if a == clean || slices.Contains(alts, a) {
				continue
			}
			if t := mediaTTL(a); t > 0 {
				alts, ttl = append(alts, a), min64(ttl, t)
			}
		}
	}
	if ttl <= 0 {
		return ""
	}
	in := "provider-media:v1:" + clean + "\x00"
	if bid != nil {
		in += *bid
	}
	for _, a := range alts {
		in += "\x00" + a
	}
	sum := sha256.Sum256([]byte(in))
	handle := "m1_" + base64.RawURLEncoding.EncodeToString(sum[:18])
	b, _ := json.Marshal(mediaTarget{URL: clean, DomandBid: bid, Alternates: alts})
	if err := s.Cache.Set(ctx, "provider-media:v1:"+handle, string(b), ttl); err != nil {
		return ""
	}
	return "/media/" + handle
}

func (s *Server) mediaRoutes(rt *Router) {
	rt.Handle("GET", "/media/{handle}", s.serveMedia)
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !mediaHandleRe.MatchString(handle) {
		httpx.Error(w, r, http.StatusNotFound, "Media handle has expired or is unknown", "media_handle_not_found")
		return
	}
	v, ok, err := s.Cache.Get(r.Context(), "provider-media:v1:"+handle)
	if err != nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "Media handle service is unavailable", "media_handle_unavailable")
		return
	}
	var t mediaTarget
	if !ok || json.Unmarshal([]byte(v), &t) != nil {
		httpx.Error(w, r, http.StatusNotFound, "Media handle has expired or is unknown", "media_handle_not_found")
		return
	}
	s.pipeMedia(w, r, append([]string{t.URL}, t.Alternates...), t.DomandBid)
}

// pipeMedia streams the first address that answers, keeping the client's Range, and moves on to the next mirror
// when one refuses or cannot be reached.
func (s *Server) pipeMedia(w http.ResponseWriter, r *http.Request, urls []string, bid *string) {
	rng := r.Header.Get("Range")
	for i, target := range urls {
		u, msg := validateProxyURL(target)
		if msg != "" {
			continue
		}
		req, _ := http.NewRequestWithContext(r.Context(), "GET", target, nil)
		switch proxyProvider(u.Hostname()) {
		case "bilibili":
			req.Header.Set("User-Agent", bilibiliUA)
			req.Header.Set("Referer", "https://www.bilibili.com")
		default:
			req.Header.Set("User-Agent", browserUA)
			if bid != nil && *bid != "" {
				req.Header.Set("Cookie", nicoBidCookie(target, *bid))
			}
		}
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		res, err := s.proxyClient.Do(req)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			continue
		}
		if res.StatusCode/100 != 2 && res.StatusCode != http.StatusRequestedRangeNotSatisfiable && i < len(urls)-1 {
			res.Body.Close()
			continue
		}
		defer res.Body.Close()
		if res.StatusCode/100 != 2 && res.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Upstream returned "+strconv.Itoa(res.StatusCode), "error")
			return
		}
		h := w.Header()
		if isHLSResponse(res.Header.Get("Content-Type"), target) && proxyProvider(u.Hostname()) != "youtube" {
			body, err := readHLS(res)
			if err != nil {
				httpx.Error(w, r, http.StatusUnprocessableEntity, "Proxy fetch failed", "error")
				return
			}
			h.Set("Content-Type", res.Header.Get("Content-Type"))
			w.WriteHeader(res.StatusCode)
			io.WriteString(w, s.rewriteProviderHLS(r.Context(), body, target, bid))
			return
		}
		ct := res.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
			if v := res.Header.Get(k); v != "" {
				h.Set(k, v)
			}
		}
		w.WriteHeader(res.StatusCode)
		if r.Method != http.MethodHead {
			copyBody(w, res.Body)
		}
		return
	}
	httpx.Error(w, r, http.StatusUnprocessableEntity, "Proxy fetch failed", "error")
}
