package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
)

// The public HLS token is PublicHlsManifestTokenService's "ph1.<url>.<expiresAt>.<mac>", keyed from the JWT secret, so
// a manifest link handed out by either server opens on the other.
func (s *Server) hlsKey() []byte {
	sum := sha256.Sum256([]byte("typetype.public-hls-manifest." + s.Cfg.JWTSecret))
	return sum[:]
}

func (s *Server) hlsSign(payload string) string {
	m := hmac.New(sha256.New, s.hlsKey())
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) hlsPath(manifest string) string {
	payload := "ph1." + base64.RawURLEncoding.EncodeToString([]byte(manifest)) + "." + strconv.FormatInt(time.Now().Add(24*time.Hour).UnixMilli(), 10)
	return "/streams/hls-manifest?token=" + payload + "." + s.hlsSign(payload)
}

// hlsVerify returns the manifest URL, or expired, or ok=false for a token that is not a public one.
func (s *Server) hlsVerify(token string) (manifest string, expired, ok bool) {
	parts := strings.Split(token, ".")
	if len(token) > 4096 || len(parts) != 4 || parts[0] != "ph1" {
		return "", false, false
	}
	got, err1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[3], "="))
	want, _ := base64.RawURLEncoding.DecodeString(s.hlsSign(strings.Join(parts[:3], ".")))
	if err1 != nil || !hmac.Equal(got, want) {
		return "", false, false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", false, false
	}
	if exp <= time.Now().UnixMilli() {
		return "", true, true
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", false, false
	}
	return string(b), false, true
}

func isManifestURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || strings.Contains(u.Path, "/file/seg.ts") {
		return false
	}
	return strings.ToLower(u.Hostname()) == "manifest.googlevideo.com" || strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

func isYouTubeLiveManifest(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !hostIs(strings.ToLower(u.Hostname()), "googlevideo.com") {
		return false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "yt_live_broadcast" {
			return true
		}
	}
	return false
}

var uriAttr = regexp.MustCompile(`URI="([^"]+)"`)

// rewriteHLS points child playlists back at /streams/hls-manifest (signed when the parent was) and media at /proxy.
func (s *Server) rewriteHLS(manifest string, signed bool) string {
	link := func(target string) string {
		if !isManifestURL(target) {
			return "../proxy?url=" + url.QueryEscape(target)
		}
		if signed {
			return s.hlsPath(target)
		}
		return "hls-manifest?url=" + url.QueryEscape(target)
	}
	lines := strings.Split(manifest, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
			lines[i] = uriAttr.ReplaceAllStringFunc(t, func(m string) string { return `URI="` + link(uriAttr.FindStringSubmatch(m)[1]) + `"` })
		default:
			lines[i] = link(t)
		}
	}
	return strings.Join(lines, "\n")
}

func (s *Server) hlsManifest(ctx context.Context, manifest string, signed bool) (string, int, string) {
	fetch, _, _ := strings.Cut(manifest, "#")
	if _, msg := validateProxyURL(fetch); msg != "" {
		return "", http.StatusBadRequest, msg
	}
	key := ""
	if !isYouTubeLiveManifest(fetch) {
		sum := sha256.Sum256([]byte(strconv.FormatBool(signed) + manifest))
		key = "go:hls-manifest:" + hex.EncodeToString(sum[:16])
		if v, ok, _ := s.Cache.Get(ctx, key); ok {
			return v, http.StatusOK, ""
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", fetch, nil)
	req.Header.Set("User-Agent", browserUA)
	res, err := s.proxyClient.Do(req)
	if err != nil {
		return "", http.StatusUnprocessableEntity, "HLS manifest fetch failed"
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return "", http.StatusUnprocessableEntity, "Upstream returned " + strconv.Itoa(res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return "", http.StatusUnprocessableEntity, "HLS manifest fetch failed"
	}
	out := s.rewriteHLS(string(b), signed)
	if key != "" {
		s.Cache.Set(ctx, key, out, 30*time.Second)
	}
	return out, http.StatusOK, ""
}

func (s *Server) hlsRoutes(rt *Router) {
	rt.Handle("GET", "/streams/hls-manifest", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		write := func(body string, status int, msg string, noStore bool) {
			if noStore {
				w.Header().Set("Cache-Control", "no-store")
			}
			if status != http.StatusOK {
				httpx.Error(w, r, status, msg, "error")
				return
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			io.WriteString(w, body)
		}
		if q.Has("token") {
			manifest, expired, ok := s.hlsVerify(q.Get("token"))
			switch {
			case !ok:
				w.Header().Set("Cache-Control", "no-store")
				httpx.Error(w, r, http.StatusServiceUnavailable, "YouTube Session is unavailable", "youtube_session_unavailable")
			case expired:
				write("", http.StatusBadRequest, "Signed HLS manifest token expired", true)
			default:
				body, status, msg := s.hlsManifest(r.Context(), manifest, true)
				write(body, status, msg, true)
			}
			return
		}
		if !q.Has("url") {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		target := q.Get("url")
		if !isManifestURL(target) {
			id := YouTubeID(target)
			if id == "" {
				write("", http.StatusBadRequest, "Invalid URL", false)
				return
			}
			in, err := s.Sabr.Token().Info(r.Context(), id)
			if err != nil || in.HlsURL == "" {
				write("", http.StatusUnprocessableEntity, "No HLS stream available for this video", false)
				return
			}
			target = in.HlsURL
		}
		body, status, msg := s.hlsManifest(r.Context(), target, false)
		write(body, status, msg, false)
	})
}
