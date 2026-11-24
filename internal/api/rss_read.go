package api

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

// rfc1123 is DateTimeFormatter.RFC_1123_DATE_TIME in UTC: the day of month is not zero-padded.
const rfc1123 = "Mon, 2 Jan 2006 15:04:05 GMT"

// rssThrottle is RssFeedThrottle: a fixed one-minute window per feed, swept every 256 requests.
type rssThrottle struct {
	mu      sync.Mutex
	windows map[string]*[2]int64
	calls   int
}

var rssLimiter = &rssThrottle{windows: map[string]*[2]int64{}}

func (t *rssThrottle) acquire(feed string, limit int, now int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calls++; t.calls%256 == 0 {
		for k, w := range t.windows {
			if now-w[0] >= 120_000 {
				delete(t.windows, k)
			}
		}
	}
	w := t.windows[feed]
	if w == nil || now-w[0] >= 60_000 {
		w = &[2]int64{now, 0}
		t.windows[feed] = w
	}
	w[1]++
	if w[1] <= int64(limit) {
		return 0
	}
	return int(max(1, (60_000-(now-w[0])+999)/1000))
}

func (s *Server) readRss(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(r.PathValue("file"), ".xml")
	secret := r.URL.Query().Get("token")
	if !ok || strings.TrimSpace(secret) == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	ctx := r.Context()
	st, err := s.Admin.Get(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !st.RssEnabled || st.RssPublicBaseURL == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	feed, err := rssFeedByID(ctx, s.DB, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if feed == nil || !feed.Enabled {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if on, err := s.rssUserEnabled(ctx, feed.userID); err != nil {
		s.fail(w, r, err)
		return
	} else if !on || subtle.ConstantTimeCompare([]byte(rssHash(secret)), []byte(feed.tokenHash)) != 1 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	now := time.Now().UnixMilli()
	if retry := rssLimiter.acquire(feed.ID, st.RssRateLimitPerMinute, now); retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		httpx.Error(w, r, http.StatusTooManyRequests, "Too many RSS requests", "rss_rate_limited")
		return
	}
	blocked, err := s.blockedFilter(r, feed.userID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.feeds.mu.Lock()
	snap := s.feeds.current[feed.userID]
	s.feeds.mu.Unlock()
	if snap == nil {
		s.feeds.refresh(feed.userID)
	}
	var scope map[string]bool
	if feed.Scope == "channels" {
		scope = map[string]bool{}
		for _, c := range feed.ChannelURLs {
			scope[c] = true
		}
	}
	videos := []youtube.VideoItem{}
	if snap != nil {
		for _, v := range snap.videos {
			if len(videos) >= st.RssMaxItems {
				break
			}
			if scope != nil && !scope[db.CanonicalChannelURL(v.UploaderURL)] || !inInts(videoServiceID(v), feed.ServiceIDs) || !rssIncludes(feed, v, now) || blocked(v) {
				continue
			}
			videos = append(videos, v)
		}
	}
	modified := feed.UpdatedAt
	for _, v := range videos {
		modified = max(modified, publishedMillis(v))
	}
	modified = min(modified, now)
	body := renderRss(feed, videos, *st.RssPublicBaseURL, modified)
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	if feed.LastUsedAt == nil || now-*feed.LastUsedAt >= 60_000 {
		s.DB.Exec(ctx, "UPDATE rss_feeds SET last_used_at = $2 WHERE id = $1", feed.ID, now)
	}
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Last-Modified", time.UnixMilli(modified).UTC().Format(rfc1123))
	h.Set("Cache-Control", "private, max-age="+strconv.Itoa(st.RssMinimumPollMinutes*60)+", must-revalidate")
	if rssUnchanged(r, etag, modified) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write(body)
}

func inInts(v int, list []int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// rssIncludes is RssVideoTypeFilter.includes.
func rssIncludes(f *rssFeed, v youtube.VideoItem, now int64) bool {
	switch {
	case v.IsLive:
		return f.IncludeLive
	case !v.IsPostLive && publishedMillis(v) > now:
		return f.IncludeUpcoming
	case v.IsShortFormContent:
		return f.IncludeShorts
	}
	return f.IncludeVideos
}

func rssUnchanged(r *http.Request, etag string, modified int64) bool {
	if inm := r.Header.Values("If-None-Match"); len(inm) > 0 {
		for _, c := range strings.Split(strings.Join(inm, ","), ",") {
			if c = strings.TrimSpace(c); c == "*" || strings.TrimPrefix(c, "W/") == strings.TrimPrefix(etag, "W/") {
				return true
			}
		}
		return false
	}
	since, err := time.Parse(rfc1123, r.Header.Get("If-Modified-Since"))
	return err == nil && !time.UnixMilli(modified).Truncate(time.Second).After(since)
}

// renderRss writes what RssDocumentRenderer's XMLStreamWriter writes: no indentation, &, < and > escaped in text
// and also " in attributes. Characters XML 1.0 forbids are dropped rather than written into an unreadable feed.
func renderRss(f *rssFeed, videos []youtube.VideoItem, base string, modified int64) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel>`)
	el := func(name, value string) {
		b.WriteString("<" + name + ">")
		xmlText(&b, value, false)
		b.WriteString("</" + name + ">")
	}
	el("title", f.Name)
	el("link", base)
	el("description", "TypeType subscription feed: "+f.Name)
	el("generator", "TypeType")
	el("lastBuildDate", time.UnixMilli(modified).UTC().Format(rfc1123))
	for _, v := range videos {
		b.WriteString("<item>")
		el("title", v.Title)
		el("link", base+"/watch?v="+javaURLEncode(v.URL))
		b.WriteString(`<guid isPermaLink="false">`)
		xmlText(&b, strconv.Itoa(videoServiceID(v))+":"+v.ID, false)
		b.WriteString("</guid>")
		el("author", v.UploaderName)
		if v.ShortDescription != nil && strings.TrimSpace(*v.ShortDescription) != "" {
			el("description", *v.ShortDescription)
		}
		if u, err := url.Parse(v.ThumbnailURL); err == nil && u.Hostname() != "" && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) &&
			!strings.ContainsFunc(v.ThumbnailURL, func(c rune) bool { return c <= ' ' || strings.ContainsRune("\"<>\\^`{|}", c) }) {
			b.WriteString(`<media:thumbnail url="`)
			xmlText(&b, v.ThumbnailURL, true)
			b.WriteString(`"></media:thumbnail>`)
		}
		if p := publishedMillis(v); p > 0 {
			el("pubDate", time.UnixMilli(p).UTC().Format(rfc1123))
		}
		b.WriteString("</item>")
	}
	b.WriteString("</channel></rss>")
	return b.Bytes()
}

func xmlText(b *bytes.Buffer, s string, attr bool) {
	for _, c := range s {
		switch {
		case c == '<':
			b.WriteString("&lt;")
		case c == '>':
			b.WriteString("&gt;")
		case c == '&':
			b.WriteString("&amp;")
		case c == '"' && attr:
			b.WriteString("&quot;")
		case c < 0x20 && c != '\t' && c != '\n' && c != '\r', c == 0xFFFE, c == 0xFFFF:
		default:
			b.WriteRune(c)
		}
	}
}

// javaURLEncode is URLEncoder.encode(s, UTF_8): letters, digits and .-*_ kept, space as +, the rest %XX.
func javaURLEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '*', c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}
