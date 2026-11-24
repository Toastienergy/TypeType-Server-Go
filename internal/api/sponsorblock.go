package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"typetype.dev/server/internal/cache"
)

type sponsorSegment struct {
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
	Category  string  `json:"category"`
	Action    string  `json:"action"`
}

type sponsorResult struct {
	segments  []sponsorSegment
	available bool
}

const (
	sponsorAPI      = "https://sponsor.ajay.app/api/"
	sponsorWait     = 2 * time.Second
	sponsorTimeout  = 15 * time.Second
	sponsorTTL      = 6 * time.Hour
	sponsorCooldown = time.Minute
)

var sponsorCategories = `["sponsor","intro","outro","interaction","poi_highlight","selfpromo","music_offtopic","preview","filler"]`

// sponsorBlock asks SponsorBlock by the first four hex digits of the id's SHA-256, so the API never learns which
// video plays. Answers are cached six hours; a slow API stops delaying the page after two seconds and still fills
// the cache; a failure pauses the API for a minute.
type sponsorBlock struct {
	cache  *cache.Cache
	client *http.Client
	mu     sync.Mutex
	until  time.Time
}

func newSponsorBlock(c *cache.Cache) *sponsorBlock {
	return &sponsorBlock{cache: c, client: &http.Client{Timeout: sponsorTimeout}}
}

func (sb *sponsorBlock) segments(ctx context.Context, id string) sponsorResult {
	key := "go:sponsorblock:" + id
	if v, ok, _ := sb.cache.Get(ctx, key); ok {
		var segs []sponsorSegment
		if json.Unmarshal([]byte(v), &segs) == nil {
			return sponsorResult{segments: segs, available: true}
		}
	}
	sb.mu.Lock()
	paused := time.Now().Before(sb.until)
	sb.mu.Unlock()
	if paused {
		return sponsorResult{segments: []sponsorSegment{}}
	}
	done := make(chan sponsorResult, 1)
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), sponsorTimeout)
		defer cancel()
		segs, err := sb.fetch(bg, id)
		if err != nil {
			sb.mu.Lock()
			sb.until = time.Now().Add(sponsorCooldown)
			sb.mu.Unlock()
			done <- sponsorResult{segments: []sponsorSegment{}}
			return
		}
		if b, err := json.Marshal(segs); err == nil {
			sb.cache.Set(bg, key, string(b), sponsorTTL)
		}
		done <- sponsorResult{segments: segs, available: true}
	}()
	select {
	case r := <-done:
		return r
	case <-time.After(sponsorWait):
		return sponsorResult{segments: []sponsorSegment{}}
	case <-ctx.Done():
		return sponsorResult{segments: []sponsorSegment{}}
	}
}

func (sb *sponsorBlock) fetch(ctx context.Context, id string) ([]sponsorSegment, error) {
	sum := sha256.Sum256([]byte(id))
	u := sponsorAPI + "skipSegments/" + hex.EncodeToString(sum[:])[:4] + "?categories=" + url.QueryEscape(sponsorCategories) +
		"&actionTypes=" + url.QueryEscape(`["skip","poi"]`) + "&userAgent=Mozilla/5.0"
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	res, err := sb.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	segs := []sponsorSegment{}
	if res.StatusCode == http.StatusNotFound {
		return segs, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("sponsorblock HTTP " + res.Status)
	}
	var videos []struct {
		VideoID  string `json:"videoID"`
		Segments []struct {
			Segment  []float64 `json:"segment"`
			Category string    `json:"category"`
			Action   string    `json:"actionType"`
		} `json:"segments"`
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &videos); err != nil {
		return nil, err
	}
	for _, v := range videos {
		if v.VideoID != id {
			continue
		}
		for _, s := range v.Segments {
			if len(s.Segment) < 2 {
				continue
			}
			segs = append(segs, sponsorSegment{StartTime: s.Segment[0] * 1000, EndTime: s.Segment[1] * 1000, Category: s.Category, Action: s.Action})
		}
	}
	return segs, nil
}
