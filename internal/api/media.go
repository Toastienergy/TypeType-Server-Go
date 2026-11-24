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
	s.warm.onServed(handle, &t, r.Header.Get("Range"))
	s.serveWarm(w, r, &t)
}

// mediaReply is an upstream answer ready to relay: a body to stream, or the rewritten text of a provider playlist.
type mediaReply struct {
	status                        int
	ctype, length, crange, accept string
	body                          io.ReadCloser
	text                          []byte
}

func (m *mediaReply) close() {
	if m != nil && m.body != nil {
		m.body.Close()
	}
}

// openMedia is pipeProviderMediaWithAlternates: the first address that answers, with the Range asked for, moving to
// the next mirror when one refuses or cannot be reached. Provider playlists come back rewritten to /media handles.
func (s *Server) openMedia(ctx context.Context, urls []string, bid *string, rng string) (*mediaReply, *commentErr) {
	if len(urls) > 0 && rng != "" {
		if u, msg := validateProxyURL(urls[0]); msg == "" && proxyProvider(u.Hostname()) == "bilibili" {
			return s.readBilibiliRange(ctx, urls[0], urls[1:], rng)
		}
	}
	for i, target := range urls {
		u, msg := validateProxyURL(target)
		if msg != "" {
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
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
			if ctx.Err() != nil {
				return nil, &commentErr{499, "", ""}
			}
			continue
		}
		if res.StatusCode/100 != 2 && res.StatusCode != http.StatusRequestedRangeNotSatisfiable && i < len(urls)-1 {
			res.Body.Close()
			continue
		}
		if res.StatusCode/100 != 2 && res.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			res.Body.Close()
			return nil, &commentErr{http.StatusUnprocessableEntity, "Upstream returned " + strconv.Itoa(res.StatusCode), "error"}
		}
		if isHLSResponse(res.Header.Get("Content-Type"), target) && proxyProvider(u.Hostname()) != "youtube" {
			body, err := readHLS(res)
			res.Body.Close()
			if err != nil {
				return nil, &commentErr{http.StatusUnprocessableEntity, "Proxy fetch failed", "error"}
			}
			text := s.rewriteProviderHLS(ctx, body, target, bid)
			return &mediaReply{status: res.StatusCode, ctype: res.Header.Get("Content-Type"), text: []byte(text)}, nil
		}
		ct := res.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		return &mediaReply{status: res.StatusCode, ctype: ct, length: res.Header.Get("Content-Length"), crange: res.Header.Get("Content-Range"),
			accept: res.Header.Get("Accept-Ranges"), body: res.Body}, nil
	}
	return nil, &commentErr{http.StatusUnprocessableEntity, "Proxy fetch failed", "error"}
}

func writeMediaReply(w http.ResponseWriter, r *http.Request, m *mediaReply) {
	defer m.close()
	h := w.Header()
	h.Set("Content-Type", m.ctype)
	for k, v := range map[string]string{"Content-Length": m.length, "Content-Range": m.crange, "Accept-Ranges": m.accept} {
		if v != "" {
			h.Set(k, v)
		}
	}
	w.WriteHeader(m.status)
	if m.text != nil {
		if r.Method != http.MethodHead {
			w.Write(m.text)
		}
		return
	}
	if r.Method != http.MethodHead {
		copyBody(w, m.body)
	}
}

// pipeMedia relays openMedia's answer.
func (s *Server) pipeMedia(w http.ResponseWriter, r *http.Request, urls []string, bid *string) {
	m, cerr := s.openMedia(r.Context(), urls, bid, r.Header.Get("Range"))
	if cerr != nil {
		if cerr.status != 499 {
			cerr.write(w, r)
		}
		return
	}
	writeMediaReply(w, r, m)
}
