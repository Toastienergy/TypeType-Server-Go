package recommender

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/recommend"
	"typetype.dev/server/internal/youtube"
)

// Loaders are the server's own lookups a plan's sources name; the recommender never asks YouTube itself.
type Loaders struct {
	Search           func(ctx context.Context, query string) []youtube.VideoItem
	ChannelVideos    func(ctx context.Context, channelURL, sort string) []youtube.VideoItem
	Trending         func(ctx context.Context) []youtube.VideoItem
	Related          func(ctx context.Context, videoURL string) (videos []youtube.VideoItem, blocked bool)
	SubscriptionFeed func(ctx context.Context, user string) []youtube.VideoItem
	KnownVideos      func(ctx context.Context, user string, urls []string) []youtube.VideoItem
}

// Candidates is RecommenderCandidateGroups: the groups sent to rank, and every video by its URL.
type Candidates struct {
	Groups      []any
	VideosByURL map[string]youtube.VideoItem
}

// Sources is RecommenderSourceFetcher with RecommenderSourceLoader and RecommenderSourceCache.
type Sources struct {
	load    Loaders
	cache   Cache
	cfg     Config
	limiter *Limiter
	metrics *Metrics
}

func NewSources(load Loaders, cache Cache, cfg Config, limiter *Limiter, metrics *Metrics) *Sources {
	return &Sources{load: load, cache: cache, cfg: cfg, limiter: limiter, metrics: metrics}
}

const feedKind = "subscription_feed"

// Fetch loads every source of the plan, at most MaxSourceParallelism at once; sources that name another ("of")
// start from the subscription feed's videos.
func (s *Sources) Fetch(ctx context.Context, user string, plan []map[string]any, seed int32) Candidates {
	gate := make(chan struct{}, s.cfg.MaxSourceParallelism)
	direct := make([][]youtube.VideoItem, len(plan))
	isDirect := make([]bool, len(plan))
	var wg sync.WaitGroup
	for i, src := range plan {
		if text(src, "of") != "" {
			continue
		}
		isDirect[i] = true
		wg.Go(func() {
			gate <- struct{}{}
			defer func() { <-gate }()
			direct[i] = s.loadSource(ctx, user, src)
		})
	}
	wg.Wait()
	var feed []youtube.VideoItem
	for i, src := range plan {
		if text(src, "kind") == feedKind {
			feed = direct[i]
			break
		}
	}
	results := make([][]youtube.VideoItem, len(plan))
	for i, src := range plan {
		if isDirect[i] {
			results[i] = direct[i]
		} else {
			results[i] = s.relatedOf(ctx, feed, src, seed, gate)
		}
	}
	out := Candidates{VideosByURL: map[string]youtube.VideoItem{}}
	for i, src := range plan {
		videos := make([]any, 0, len(results[i]))
		for _, v := range results[i] {
			videos = append(videos, Candidate(v))
			out.VideosByURL[v.URL] = v
		}
		out.Groups = append(out.Groups, map[string]any{"source": src, "videos": videos})
	}
	if out.Groups == nil {
		out.Groups = []any{}
	}
	return out
}

func (s *Sources) loadSource(ctx context.Context, user string, src map[string]any) []youtube.VideoItem {
	var videos []youtube.VideoItem
	switch text(src, "kind") {
	case "search":
		if q := text(src, "query"); q != "" {
			videos = s.cached(ctx, "search:"+q, s.cfg.SearchTTL, func() []youtube.VideoItem { return s.load.Search(ctx, q) })
		}
	case "related":
		if u := text(src, "video_url"); u != "" {
			videos = s.Related(ctx, u)
		}
	case "channel_videos":
		if u := text(src, "channel_url"); u != "" {
			sort := text(src, "sort")
			videos = s.cached(ctx, "channel:"+sort+":"+u, s.cfg.ChannelTTL, func() []youtube.VideoItem { return s.load.ChannelVideos(ctx, u, sort) })
		}
	case feedKind:
		videos = s.load.SubscriptionFeed(ctx, user)
	case "trending":
		videos = s.cached(ctx, "trending", s.cfg.ChannelTTL, func() []youtube.VideoItem { return s.load.Trending(ctx) })
	case "videos":
		urls, _ := src["urls"].([]any)
		var list []string
		for _, u := range urls {
			if t, ok := u.(string); ok {
				list = append(list, t)
			}
		}
		if len(list) > 0 {
			videos = s.load.KnownVideos(ctx, user, list)
		}
	}
	return upcomingDropped(videos, number64(src, "limit"))
}

// Related is the related videos of one video, through the limiter: past the limit or behind YouTube's wall the page
// is built without them.
func (s *Sources) Related(ctx context.Context, videoURL string) []youtube.VideoItem {
	return s.cached(ctx, "related:"+videoURL, s.cfg.RelatedTTL, func() []youtube.VideoItem {
		if !s.limiter.TryAcquire(ctx) {
			s.metrics.StreamRequestsRefused.Add(1)
			return nil
		}
		s.metrics.StreamRequests.Add(1)
		videos, blocked := s.load.Related(ctx, videoURL)
		s.limiter.Opened(ctx, true, blocked)
		var out []youtube.VideoItem
		for _, v := range videos {
			if v.URL != "" && v.URL != videoURL {
				out = append(out, v)
			}
		}
		return out
	})
}

func (s *Sources) relatedOf(ctx context.Context, feed []youtube.VideoItem, src map[string]any, seed int32, gate chan struct{}) []youtube.VideoItem {
	count := int(number64(src, "count"))
	if _, ok := src["count"]; !ok {
		count = 1
	}
	starts := feed
	if among := number64(src, "among"); among >= 0 {
		starts = recommend.Shuffled(feed[:min(int(among), len(feed))], seed)
	}
	starts = starts[:min(max(count, 0), len(starts))]
	limit := number64(src, "limit")
	parts := make([][]youtube.VideoItem, len(starts))
	var wg sync.WaitGroup
	for i, start := range starts {
		wg.Go(func() {
			gate <- struct{}{}
			defer func() { <-gate }()
			parts[i] = upcomingDropped(s.Related(ctx, start.URL), limit)
		})
	}
	wg.Wait()
	var out []youtube.VideoItem
	seen := map[string]bool{}
	for _, p := range parts {
		for _, v := range p {
			if !seen[v.URL] {
				seen[v.URL] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// cached keeps what a source returned for every user (YouTube answers the same to everyone without an account); an
// empty answer is not kept, it may be a refusal.
func (s *Sources) cached(ctx context.Context, key string, ttl time.Duration, load func() []youtube.VideoItem) []youtube.VideoItem {
	cacheKey := "recommender:source:v1:" + key
	if raw, ok, _ := s.cache.Get(ctx, cacheKey); ok {
		var videos []youtube.VideoItem
		if json.Unmarshal([]byte(raw), &videos) == nil {
			s.metrics.SourceCacheHits.Add(1)
			return videos
		}
	}
	s.metrics.SourceRequests.Add(1)
	videos := load()
	if len(videos) > 0 && ttl > 0 {
		if b, err := json.Marshal(videos); err == nil {
			s.cache.Set(ctx, cacheKey, string(b), ttl)
		}
	}
	return videos
}

// Candidate is VideoItem.toRecommenderCandidate: a video as the recommender's contract reads it.
func Candidate(v youtube.VideoItem) map[string]any {
	c := map[string]any{"url": v.URL, "title": v.Title, "uploader_url": v.UploaderURL, "uploader_name": v.UploaderName}
	if v.Duration > 0 {
		c["duration_s"] = v.Duration
	}
	if v.ViewCount >= 0 {
		c["view_count"] = v.ViewCount
	}
	if at := uploadedAt(v); at > 0 {
		c["uploaded_at"] = at
	}
	c["is_short"] = v.IsShortFormContent || isShortsURL(v.URL)
	return c
}

func uploadedAt(v youtube.VideoItem) int64 {
	if v.PublishedAt != nil && *v.PublishedAt > 0 {
		return *v.PublishedAt
	}
	return max(v.Uploaded, 0)
}

// StartsLater is VideoItem.startsLater: a premiere or an upcoming live.
func StartsLater(v youtube.VideoItem, nowMs int64) bool {
	at := uploadedAt(v)
	return at > 0 && at > nowMs
}

func upcomingDropped(videos []youtube.VideoItem, limit int64) []youtube.VideoItem {
	now := time.Now().UnixMilli()
	var out []youtube.VideoItem
	for _, v := range videos {
		if limit >= 0 && int64(len(out)) >= limit {
			break
		}
		if !StartsLater(v, now) {
			out = append(out, v)
		}
	}
	return out
}

func text(m map[string]any, name string) string {
	s, _ := m[name].(string)
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return s
}

// number64 reads an integer member; -1 when absent, which reads as no limit.
func number64(m map[string]any, name string) int64 {
	n, ok := m[name].(float64)
	if !ok {
		return -1
	}
	return int64(n)
}
