package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/niconico"
)

const hlsMime = "application/vnd.apple.mpegurl"

type audioOnlyResponse struct {
	Src           string  `json:"src"`
	Kind          string  `json:"kind"`
	MimeType      string  `json:"mimeType"`
	Codec         *string `json:"codec"`
	Bitrate       *int64  `json:"bitrate"`
	ContentLength *int64  `json:"contentLength"`
	Duration      *int64  `json:"duration"`
}

// captured holds the answer of a route called from inside the server.
type captured struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *captured) Header() http.Header { return c.header }
func (c *captured) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(b)
}
func (c *captured) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

// streamFor answers what the stream route of raw's service answers, by calling that route, so the access, blocked
// and Focus guards are exactly those of playback. Anything but a 200 comes back as is for the caller to relay.
func (s *Server) streamFor(r *http.Request, raw string) (*streamResponse, *captured) {
	c := &captured{header: http.Header{}}
	sub := r.Clone(r.Context())
	sub.URL = &url.URL{Path: r.URL.Path, RawQuery: "url=" + url.QueryEscape(raw)}
	switch {
	case YouTubeID(raw) != "":
		s.youtubeStream(c, sub, false)
	case bilibiliHost(raw):
		s.bilibiliStream(c, sub)
	case isNiconico(raw):
		s.niconicoStream(c, sub)
	default:
		c.header.Set("Content-Type", "application/json")
		c.status = http.StatusBadRequest
		json.NewEncoder(&c.body).Encode(map[string]string{"error": "Unsupported service", "code": "unsupported_service"})
	}
	if c.status != http.StatusOK {
		return nil, c
	}
	var resp streamResponse
	if json.Unmarshal(c.body.Bytes(), &resp) != nil {
		return nil, c
	}
	return &resp, nil
}

func isNiconico(raw string) bool {
	l := strings.ToLower(raw)
	return niconico.IDRe.MatchString(raw) && (strings.Contains(l, "nicovideo.jp") || strings.Contains(l, "nico.ms"))
}

func relay(w http.ResponseWriter, c *captured) {
	for k, v := range c.header {
		w.Header()[k] = v
	}
	w.WriteHeader(c.status)
	w.Write(c.body.Bytes())
}

// audioRank is AudioOnlyStreamSelector's preference: the original track when asked, then the wanted language, then
// the default track, then any original one; higher bitrate first within a rank.
func audioRank(a *audioStream, resp *streamResponse, preferOriginal bool, locale string) int {
	norm := func(v string) string {
		v, _, _ = strings.Cut(strings.ReplaceAll(strings.ToLower(v), "_", "-"), "-")
		return v
	}
	switch {
	case preferOriginal && a.IsOriginal:
		return 0
	case locale != "" && (a.AudioLocale != nil && norm(*a.AudioLocale) == norm(locale) ||
		a.AudioTrackID != nil && norm(strings.SplitN(*a.AudioTrackID, ".", 2)[0]) == norm(locale)):
		return 1
	case a.AudioTrackID != nil && resp.PreferredDefaultAudioTrackID != nil && *a.AudioTrackID == *resp.PreferredDefaultAudioTrackID:
		return 2
	case !preferOriginal && a.IsOriginal:
		return 3
	}
	return 4
}

func aacAudio(a *audioStream) bool {
	mime, _, _ := strings.Cut(strings.ToLower(a.MimeType), ";")
	c := ""
	if a.Codec != nil {
		c = strings.ToLower(*a.Codec)
	}
	return strings.TrimSpace(mime) == "audio/mp4" && (c == "" || strings.HasPrefix(c, "mp4a"))
}

// pickAudio is the best AAC track the client can play, optionally the one a token already chose.
func pickAudio(resp *streamResponse, preferOriginal bool, locale string, keep func(*audioStream) bool) *audioStream {
	var out []*audioStream
	for i := range resp.AudioStreams {
		a := &resp.AudioStreams[i]
		if a.URL == "" && a.SabrSessionURL == nil || !aacAudio(a) || !keep(a) {
			continue
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := audioRank(out[i], resp, preferOriginal, locale), audioRank(out[j], resp, preferOriginal, locale)
		if ri != rj {
			return ri < rj
		}
		return deref(out[i].Bitrate) > deref(out[j].Bitrate)
	})
	if len(out) == 0 {
		return nil
	}
	return out[0]
}

type audioSourceEntry struct {
	Path string `json:"path"`
	Mime string `json:"mime"`
}

// typedWriter answers with the track's own type, as Kotlin's contentTypeOverride does: providers serve audio as
// video/mp4 or octet-stream.
type typedWriter struct {
	http.ResponseWriter
	mime  string
	wrote bool
}

func (t *typedWriter) WriteHeader(code int) {
	if t.wrote {
		return
	}
	t.wrote = true
	if code/100 == 2 && t.mime != "" {
		t.Header().Set("Content-Type", t.mime)
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *typedWriter) Write(b []byte) (int, error) {
	t.WriteHeader(http.StatusOK)
	return t.ResponseWriter.Write(b)
}

func (t *typedWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func sourceKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "audio-only-source:v1:" + hex.EncodeToString(sum[:])
}

func (s *Server) audioOnly(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw := q.Get("url")
	if raw == "" {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	preferOriginal, locale := strings.EqualFold(q.Get("preferOriginal"), "true"), strings.TrimSpace(q.Get("preferredLocale"))
	resp, fail := s.streamFor(r, raw)
	if fail != nil {
		relay(w, fail)
		return
	}
	a := pickAudio(resp, preferOriginal, locale, func(*audioStream) bool { return true })
	if a == nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "No audio-only stream is available", "no_playable_streams")
		return
	}
	user := s.optionalUserID(r)
	token := s.createAudioToken(audioToken{UserID: user, VideoURL: raw, PreferOriginal: preferOriginal, Locale: locale,
		Itag: a.Itag, TrackID: deref(a.AudioTrackID)})
	out := audioOnlyResponse{Codec: a.Codec, Bitrate: a.Bitrate}
	if resp.Duration > 0 {
		out.Duration = &resp.Duration
	}
	switch {
	case a.DeliveryMethod == "sabr":
		out.Kind, out.MimeType = "hls", hlsMime
		out.Src = "/sabr/manifest/" + YouTubeID(raw) + "?audioOnly=true&format=hls&audioToken=" + url.QueryEscape(token)
	case isNiconico(raw):
		// NicoNico audio is an HLS rendition behind its media handle, which rewrites keys and segments itself.
		out.Kind, out.MimeType, out.Src = "hls", hlsMime, a.URL
	default:
		src, _ := json.Marshal(audioSourceEntry{Path: a.URL, Mime: a.MimeType})
		if err := s.Cache.Set(r.Context(), sourceKey(token), string(src), audioTokenTTL); err != nil {
			httpx.Error(w, r, http.StatusServiceUnavailable, "No audio-only stream is available", "no_playable_streams")
			return
		}
		out.Kind, out.MimeType = "progressive", a.MimeType
		out.Src = "/streams/audio-only/source?token=" + url.QueryEscape(token)
		if a.ContentLength > 0 {
			out.ContentLength = &a.ContentLength
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, out)
}

// audioSource serves a progressive track chosen by /streams/audio-only, through the media handle the token was
// issued for, with the client's Range.
func (s *Server) audioSource(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("token")
	if raw == "" {
		plain(w, r, http.StatusBadRequest, "Missing 'token' parameter")
		return
	}
	if _, msg := s.verifyAudioToken(raw); msg != "" {
		plain(w, r, http.StatusUnauthorized, msg)
		return
	}
	v, ok, err := s.Cache.Get(r.Context(), sourceKey(raw))
	var e audioSourceEntry
	if err != nil || !ok || json.Unmarshal([]byte(v), &e) != nil || !strings.HasPrefix(e.Path, "/media/") {
		plain(w, r, http.StatusUnauthorized, "Audio-only token expired")
		return
	}
	path := e.Path
	w = &typedWriter{ResponseWriter: w, mime: e.Mime}
	sub := r.Clone(r.Context())
	sub.URL = &url.URL{Path: path}
	sub.SetPathValue("handle", strings.TrimPrefix(path, "/media/"))
	if r.Method == http.MethodHead {
		c := &captured{header: http.Header{}}
		sub.Method = http.MethodGet
		sub.Header.Set("Range", "bytes=0-0")
		s.serveMedia(c, sub)
		for _, k := range []string{"Content-Type", "Accept-Ranges"} {
			if v := c.header.Get(k); v != "" {
				w.Header().Set(k, v)
			}
		}
		if total := strings.SplitN(c.header.Get("Content-Range"), "/", 2); len(total) == 2 {
			if _, err := strconv.ParseInt(total[1], 10, 64); err == nil {
				w.Header().Set("Content-Length", total[1])
			}
		}
		status := c.status
		if status == http.StatusPartialContent {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		return
	}
	s.serveMedia(w, sub)
}
