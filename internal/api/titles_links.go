package api

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/httpx"
)

var (
	b23CodeRe   = regexp.MustCompile(`^[A-Za-z0-9]{4,16}$`)
	titleBVRe   = regexp.MustCompile(`^BV[0-9A-Za-z]{10}$`)
	titleAVRe   = regexp.MustCompile(`(?i)^av([1-9][0-9]{0,14})$`)
	titleNicoRe = regexp.MustCompile(`(?i)^(sm|so|nm)([1-9][0-9]{0,11})$`)
	xmlTitleRe  = regexp.MustCompile(`<title>([^<]*)</title>`)
)

// shortLinks follows a b23.tv link's single redirect and keeps the target only when it is a bilibili.com page.
type shortLinks struct {
	mu     sync.Mutex
	known  map[string]string
	client *http.Client
}

func newShortLinks() *shortLinks {
	return &shortLinks{known: map[string]string{}, client: &http.Client{Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (l *shortLinks) resolve(ctx context.Context, code string) string {
	if !b23CodeRe.MatchString(code) {
		return ""
	}
	l.mu.Lock()
	if t, ok := l.known[code]; ok {
		l.mu.Unlock()
		return t
	}
	l.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://b23.tv/"+code, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")
	res, err := l.client.Do(req)
	if err != nil {
		return ""
	}
	res.Body.Close()
	switch res.StatusCode {
	case 301, 302, 303, 307, 308:
	default:
		return ""
	}
	u, err := url.Parse(res.Header.Get("Location"))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h != "bilibili.com" && !strings.HasSuffix(h, ".bilibili.com") {
		return ""
	}
	p := u.Query().Get("p")
	u.RawQuery, u.Fragment = "", ""
	if p != "" {
		u.RawQuery = "p=" + url.QueryEscape(p)
	}
	t := u.String()
	l.mu.Lock()
	if len(l.known) > 512 {
		l.known = map[string]string{}
	}
	l.known[code] = t
	l.mu.Unlock()
	return t
}

func normalizeTitleID(raw string) string {
	id := strings.TrimSpace(raw)
	switch {
	case titleBVRe.MatchString(id):
		return id
	case titleAVRe.MatchString(id):
		return "av" + titleAVRe.FindStringSubmatch(id)[1]
	case titleNicoRe.MatchString(id):
		m := titleNicoRe.FindStringSubmatch(id)
		return strings.ToLower(m[1]) + m[2]
	}
	return ""
}

// videoTitle is VideoTitleService's lookup: Bilibili's view API for BV/av ids, NicoNico's thumb info for the rest.
// A miss is remembered as an empty title so a dead id is not asked again for twelve hours.
func (s *Server) videoTitle(ctx context.Context, id string) string {
	key := "go:video-title:" + id
	if v, ok, _ := s.Cache.Get(ctx, key); ok {
		return v
	}
	title := ""
	if strings.HasPrefix(id, "BV") || strings.HasPrefix(id, "av") {
		q := "bvid=" + id
		if strings.HasPrefix(id, "av") {
			q = "aid=" + strings.TrimPrefix(id, "av")
		}
		if res, err := s.bili.Get(ctx, "https://api.bilibili.com/x/web-interface/view?"+q, "https://www.bilibili.com/"); err == nil {
			if d, ok := res["data"].(map[string]any); ok {
				title, _ = d["title"].(string)
			}
		}
	} else {
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://ext.nicovideo.jp/api/getthumbinfo/"+id, nil)
		if res, err := s.proxyClient.Do(req); err == nil {
			b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			res.Body.Close()
			if strings.Contains(string(b), `status="ok"`) {
				if m := xmlTitleRe.FindSubmatch(b); m != nil {
					title = html.UnescapeString(string(m[1]))
				}
			}
		}
	}
	title = strings.TrimSpace(title)
	s.Cache.Set(ctx, key, title, 12*time.Hour)
	return title
}

func (s *Server) titleLinkRoutes(rt *Router) {
	rt.Handle("GET", "/videos/titles", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		var ids []string
		seen := map[string]bool{}
		for _, raw := range strings.Split(r.URL.Query().Get("ids"), ",") {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			if id := normalizeTitleID(raw); id != "" && !seen[id] && len(ids) < 30 {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if strings.Trim(r.URL.Query().Get("ids"), ", ") == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'ids' parameter")
			return
		}
		titles := map[string]string{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		slots := make(chan struct{}, 4)
		for _, id := range ids {
			wg.Go(func() {
				slots <- struct{}{}
				defer func() { <-slots }()
				if t := s.videoTitle(r.Context(), id); t != "" {
					mu.Lock()
					titles[id] = t
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		httpx.JSON(w, http.StatusOK, map[string]any{"titles": titles})
	})
	rt.Handle("GET", "/links/b23/{code}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		t := s.links.resolve(r.Context(), r.PathValue("code"))
		if t == "" {
			plain(w, r, http.StatusNotFound, "Unknown short link")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"url": t})
	})
}
