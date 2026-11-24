package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
)

// dearrow is DeArrowService: community titles and thumbnails from the DeArrow API, under the Kotlin cache keys.
type dearrow struct {
	s       *Server
	client  *http.Client
	permits chan struct{}
}

type deArrowTitle struct {
	Title    string `json:"title"`
	Original bool   `json:"original"`
	Votes    int    `json:"votes"`
	Locked   bool   `json:"locked"`
	UUID     string `json:"uuid"`
}

type deArrowThumb struct {
	Timestamp    *float64 `json:"timestamp"`
	ThumbnailURL *string  `json:"thumbnailUrl"`
	Original     bool     `json:"original"`
	Votes        int      `json:"votes"`
	Locked       bool     `json:"locked"`
	UUID         string   `json:"uuid"`
}

type deArrowItem struct {
	VideoID        string         `json:"videoId"`
	Title          *string        `json:"title"`
	ThumbnailURL   *string        `json:"thumbnailUrl"`
	Titles         []deArrowTitle `json:"titles"`
	Thumbnails     []deArrowThumb `json:"thumbnails"`
	RandomTime     *float64       `json:"randomTime"`
	VideoDuration  *float64       `json:"videoDuration"`
	AttributionURL string         `json:"attributionUrl"`
}

var (
	errDeArrowDown = errors.New("dearrow unavailable")
	titleMarker    = regexp.MustCompile(`>(\S)`)
)

func newDeArrow(s *Server) *dearrow {
	return &dearrow{s: s, client: &http.Client{Timeout: 10 * time.Second}, permits: make(chan struct{}, 6)}
}

// javaDouble prints a double the way Kotlin's toString does, since it lands in URLs the clients compare.
func javaDouble(f float64) string {
	if f != 0 && (math.Abs(f) < 1e-3 || math.Abs(f) >= 1e7) {
		m, e, _ := strings.Cut(strconv.FormatFloat(f, 'E', -1, 64), "E")
		if !strings.Contains(m, ".") {
			m += ".0"
		}
		n, _ := strconv.Atoi(e)
		return m + "E" + strconv.Itoa(n)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func (d *dearrow) get(ctx context.Context, u string) ([]byte, int, error) {
	d.permits <- struct{}{}
	defer func() { <-d.permits }()
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "TypeType-DeArrow/1.0")
	res, err := d.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return b, res.StatusCode, err
}

func thumbPath(id string, t float64) *string {
	p := "/dearrow/thumbnail?videoId=" + id + "&time=" + javaDouble(t)
	return &p
}

func parseDeArrow(id string, raw []byte) deArrowItem {
	item := deArrowItem{VideoID: id, Titles: []deArrowTitle{}, Thumbnails: []deArrowThumb{}, AttributionURL: "https://dearrow.ajay.app"}
	var root struct {
		Titles []struct {
			Title    *string `json:"title"`
			Original any     `json:"original"`
			Votes    any     `json:"votes"`
			Locked   any     `json:"locked"`
			UUID     any     `json:"UUID"`
		} `json:"titles"`
		Thumbnails []struct {
			Timestamp *float64 `json:"timestamp"`
			Original  any      `json:"original"`
			Votes     any      `json:"votes"`
			Locked    any      `json:"locked"`
			UUID      any      `json:"UUID"`
		} `json:"thumbnails"`
		RandomTime    *float64 `json:"randomTime"`
		VideoDuration *float64 `json:"videoDuration"`
	}
	if json.Unmarshal(raw, &root) != nil {
		return item
	}
	flag := func(v any) bool { return fmt.Sprint(v) == "true" }
	votes := func(v any) int {
		if f, ok := v.(float64); ok && f == math.Trunc(f) {
			return int(f)
		}
		if n, err := strconv.Atoi(fmt.Sprint(v)); err == nil {
			return n
		}
		return -1
	}
	str := func(v any) string {
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	for _, t := range root.Titles {
		if t.Title == nil {
			continue
		}
		title := titleMarker.ReplaceAllString(*t.Title, "$1")
		if strings.TrimSpace(title) == "" {
			continue
		}
		c := deArrowTitle{Title: title, Original: flag(t.Original), Votes: votes(t.Votes), Locked: flag(t.Locked), UUID: str(t.UUID)}
		item.Titles = append(item.Titles, c)
		if item.Title == nil && (c.Locked || c.Votes >= 0) && !c.Original {
			item.Title = &c.Title
		}
	}
	var chosen *float64
	for _, t := range root.Thumbnails {
		c := deArrowThumb{Timestamp: t.Timestamp, Original: flag(t.Original), Votes: votes(t.Votes), Locked: flag(t.Locked), UUID: str(t.UUID)}
		if t.Timestamp != nil && *t.Timestamp > 0 {
			c.ThumbnailURL = thumbPath(id, *t.Timestamp)
		}
		item.Thumbnails = append(item.Thumbnails, c)
		if chosen == nil && (c.Locked || c.Votes >= 0) && !c.Original && c.Timestamp != nil {
			chosen = c.Timestamp
		}
	}
	if chosen == nil && root.VideoDuration != nil && root.RandomTime != nil {
		v := *root.VideoDuration * *root.RandomTime
		chosen = &v
	}
	if chosen != nil && *chosen > 0 {
		item.ThumbnailURL = thumbPath(id, *chosen)
	}
	item.RandomTime, item.VideoDuration = root.RandomTime, root.VideoDuration
	return item
}

func (d *dearrow) branding(ctx context.Context, id string) (deArrowItem, error) {
	key := "dearrow:branding:v3:" + id
	if v, ok, _ := d.s.Cache.Get(ctx, key); ok {
		item := deArrowItem{AttributionURL: "https://dearrow.ajay.app", Titles: []deArrowTitle{}, Thumbnails: []deArrowThumb{}}
		if json.Unmarshal([]byte(v), &item) == nil {
			return item, nil
		}
	}
	body, status, err := d.get(ctx, "https://sponsor.ajay.app/api/branding?videoID="+id+"&fetchAll=true")
	if err != nil || status != http.StatusNotFound && status/100 != 2 {
		return deArrowItem{}, errDeArrowDown
	}
	item := deArrowItem{VideoID: id, Titles: []deArrowTitle{}, Thumbnails: []deArrowThumb{}, AttributionURL: "https://dearrow.ajay.app"}
	if status/100 == 2 && strings.TrimSpace(string(body)) != "" {
		item = parseDeArrow(id, body)
	}
	b, _ := json.Marshal(item)
	d.s.Cache.Set(ctx, key, string(b), 24*time.Hour)
	return item, nil
}

func (d *dearrow) thumbnail(ctx context.Context, id string, t float64) ([]byte, bool, bool) {
	key := "dearrow:thumbnail:" + id + ":" + strconv.FormatFloat(t, 'f', 3, 64)
	if v, ok, _ := d.s.Cache.Get(ctx, key); ok {
		fallback := strings.HasPrefix(v, "fallback:")
		payload := strings.TrimPrefix(strings.TrimPrefix(v, "fallback:"), "generated:")
		if b, err := base64.StdEncoding.DecodeString(payload); err == nil {
			return b, fallback, true
		}
	}
	ok200 := func(b []byte, status int, err error) []byte {
		if err != nil || status/100 != 2 || len(b) == 0 || len(b) > 2<<20 {
			return nil
		}
		return b
	}
	img := ok200(d.get(ctx, "https://dearrow-thumb.ajay.app/api/v1/getThumbnail?videoID="+id+"&time="+javaDouble(t)))
	fallback := img == nil
	if fallback {
		img = ok200(d.get(ctx, "https://i.ytimg.com/vi/"+id+"/hqdefault.jpg"))
	}
	if img == nil {
		return nil, false, false
	}
	prefix, ttl := "generated:", 7*24*time.Hour
	if fallback {
		prefix, ttl = "fallback:", 15*time.Minute
	}
	d.s.Cache.Set(ctx, key, prefix+base64.StdEncoding.EncodeToString(img), ttl)
	return img, fallback, true
}

func (s *Server) dearrowRoutes(rt *Router) {
	rt.Handle("GET", "/dearrow", s.zones.dearrow.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("videoId")
		if !videoIDRe.MatchString(id) {
			plain(w, r, http.StatusBadRequest, "Invalid videoId")
			return
		}
		item, err := s.deArrow.branding(r.Context(), id)
		if err != nil {
			w.Header().Set("Retry-After", "30")
			httpx.Error(w, r, http.StatusServiceUnavailable, "DeArrow is temporarily unavailable", "dearrow_unavailable")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		httpx.JSON(w, http.StatusOK, item)
	}))
	rt.Handle("GET", "/dearrow/thumbnail", s.zones.dearrow.Limit(nil, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("videoId")
		t, err := strconv.ParseFloat(r.URL.Query().Get("time"), 64)
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Invalid time")
			return
		}
		if !videoIDRe.MatchString(id) || math.IsInf(t, 0) || math.IsNaN(t) || t < 0 {
			plain(w, r, http.StatusNotFound, "Thumbnail not found")
			return
		}
		img, fallback, ok := s.deArrow.thumbnail(r.Context(), id, t)
		if !ok {
			plain(w, r, http.StatusNotFound, "Thumbnail not found")
			return
		}
		age := "604800"
		if fallback {
			age = "900"
		}
		w.Header().Set("Cache-Control", "public, max-age="+age)
		ct := "image/jpeg"
		if len(img) >= 12 && string(img[:4]) == "RIFF" && string(img[8:12]) == "WEBP" {
			ct = "image/webp"
		}
		w.Header().Set("Content-Type", ct)
		w.Write(img)
	}))
}
