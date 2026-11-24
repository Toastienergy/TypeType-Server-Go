package api

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var emojiCodeRe = regexp.MustCompile(`^[0-9A-F]{2,6}(-[0-9A-F]{2,6})*$`)

// normalizeEmojiCode is AvatarService.normalizeEmojiCode: upper case, at most four parts, FE0F dropped.
func normalizeEmojiCode(raw string) string {
	n := strings.ToUpper(strings.TrimSpace(raw))
	if !emojiCodeRe.MatchString(n) {
		return ""
	}
	parts := strings.Split(n, "-")
	if len(parts) > 4 {
		return ""
	}
	kept := parts[:0]
	for _, p := range parts {
		if p != "FE0F" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, "-")
}

const openMojiCDN = "https://cdn.jsdelivr.net/gh/hfg-gmuend/openmoji@master/color/svg/"

// openMoji serves OpenMoji SVGs from memory, then Dragonfly, then the CDN; a missing code is remembered for an hour
// and a failing CDN for fifteen seconds, so a broken avatar never hammers it.
type openMoji struct {
	mu       sync.Mutex
	local    map[string][]byte
	notFound map[string]time.Time
	failed   map[string]time.Time
	client   *http.Client
}

func newOpenMoji() *openMoji {
	return &openMoji{local: map[string][]byte{}, notFound: map[string]time.Time{}, failed: map[string]time.Time{},
		client: &http.Client{Timeout: 10 * time.Second}}
}

func (s *Server) openMojiSVG(ctx context.Context, code string) []byte {
	o := s.emoji
	o.mu.Lock()
	if b, ok := o.local[code]; ok {
		o.mu.Unlock()
		return b
	}
	if t, ok := o.notFound[code]; ok && time.Since(t) < time.Hour {
		o.mu.Unlock()
		return nil
	}
	if t, ok := o.failed[code]; ok && time.Since(t) < 15*time.Second {
		o.mu.Unlock()
		return nil
	}
	o.mu.Unlock()
	key := "go:openmoji:" + code
	if v, ok, _ := s.Cache.Get(ctx, key); ok {
		o.remember(code, []byte(v))
		return []byte(v)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", openMojiCDN+code+".svg", nil)
	res, err := o.client.Do(req)
	if err != nil {
		o.mark(o.failed, code)
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		o.mark(o.notFound, code)
		return nil
	}
	if res.StatusCode != http.StatusOK {
		o.mark(o.failed, code)
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		o.mark(o.failed, code)
		return nil
	}
	o.remember(code, b)
	s.Cache.Set(ctx, key, string(b), 30*24*time.Hour)
	return b
}

func (o *openMoji) remember(code string, b []byte) {
	o.mu.Lock()
	if len(o.local) > 512 {
		o.local = map[string][]byte{}
	}
	o.local[code] = b
	o.mu.Unlock()
}

func (o *openMoji) mark(m map[string]time.Time, code string) {
	o.mu.Lock()
	if len(m) > 1024 {
		clear(m)
	}
	m[code] = time.Now()
	o.mu.Unlock()
}

func textResponse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.WriteHeader(status)
	io.WriteString(w, msg)
}

func (s *Server) avatarRoutes(rt *Router) {
	rt.Handle("GET", "/avatar/openmoji/{code}.svg", func(w http.ResponseWriter, r *http.Request) {
		code := normalizeEmojiCode(r.PathValue("code"))
		if code == "" {
			textResponse(w, http.StatusBadRequest, "Invalid OpenMoji code")
			return
		}
		b := s.openMojiSVG(r.Context(), code)
		if b == nil {
			textResponse(w, http.StatusNotFound, "Avatar not found")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(b)
	})
	rt.Handle("GET", "/avatar/custom/{userId}/{version}", func(w http.ResponseWriter, r *http.Request) {
		var media, version string
		var content []byte
		err := s.DB.QueryRow(r.Context(), "SELECT media_type, content, version FROM user_avatars WHERE user_id = $1", r.PathValue("userId")).
			Scan(&media, &content, &version)
		if err != nil || version != r.PathValue("version") {
			textResponse(w, http.StatusNotFound, "Avatar not found")
			return
		}
		etag := `"` + version + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", media)
		w.Write(content)
	})
}
