package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"typetype.dev/server/internal/recommend"
	"typetype.dev/server/internal/store"
	"typetype.dev/server/internal/youtube"
)

// recPools is HomeRecommendationPoolResolver: a pool lives an hour in the cache and a day as a stale copy; a miss
// serves the stale copy or a fast pool from the cached subscription feed while the full pool builds in the background.
type recPools struct {
	s        *Server
	mu       sync.Mutex
	building map[string]bool
	trend    map[int]trendEntry
	// decoded spares decoding the pool, a few hundred KB, on every home or Shorts page.
	decoded decodedValues[*recommend.Pool]
}

type trendEntry struct {
	items []youtube.VideoItem
	at    time.Time
}

const (
	poolTTL      = time.Hour
	poolStaleTTL = 24 * time.Hour
)

func newRecPools(s *Server) *recPools {
	return &recPools{s: s, building: map[string]bool{}, trend: map[int]trendEntry{}}
}

type signals struct {
	favorites, history, subscriptions []string
}

func poolKey(user string, svc int, shorts bool) string {
	mode := "FULL"
	if shorts {
		mode = "SHORTS"
	}
	sum := sha256.Sum256([]byte("go1:" + user + ":" + strconv.Itoa(svc) + ":" + mode + ":false"))
	return "recommendations:home:" + hex.EncodeToString(sum[:])
}

func (p *recPools) read(ctx context.Context, key string) (*recommend.Pool, bool) {
	v, ok, _ := p.s.Cache.Get(ctx, key)
	if !ok {
		return nil, false
	}
	pool, ok := p.decoded.decode(key, v)
	return pool, ok && pool != nil
}

func (p *recPools) trending(ctx context.Context, svc int) []youtube.VideoItem {
	p.mu.Lock()
	if e, ok := p.trend[svc]; ok && time.Since(e.at) < 30*time.Minute {
		p.mu.Unlock()
		return e.items
	}
	p.mu.Unlock()
	var t []youtube.VideoItem
	var err error
	switch svc {
	case 0:
		t, err = p.s.YT.Trending(ctx)
	case 5:
		t, err = p.s.bilibiliTrending(ctx)
	case 6:
		t, err = p.s.niconicoTrending(ctx)
	}
	if err != nil {
		return nil
	}
	p.mu.Lock()
	p.trend[svc] = trendEntry{t, time.Now()}
	p.mu.Unlock()
	return t
}

func (p *recPools) profile(r *http.Request, user string) (*recommend.Profile, *signals, error) {
	ctx := r.Context()
	pr := &recommend.Profile{Seen: map[string]bool{}, Subscriptions: map[string]bool{}, Favorites: map[string]bool{}, WatchLater: map[string]bool{}}
	sig := &signals{}
	rows, err := p.s.DB.Query(ctx, `SELECT 's', channel_url FROM subscriptions WHERE user_id = $1
UNION ALL SELECT 'f', video_url FROM favorites WHERE user_id = $1
UNION ALL SELECT 'w', url FROM watch_later WHERE user_id = $1
UNION ALL (SELECT 'h', url FROM history WHERE user_id = $1 ORDER BY watched_at DESC LIMIT 240)`, user)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var kind, v string
		if rows.Scan(&kind, &v) != nil {
			continue
		}
		switch kind {
		case "s":
			pr.Subscriptions[v] = true
			sig.subscriptions = append(sig.subscriptions, v)
		case "f":
			pr.Favorites[v] = true
			sig.favorites = append(sig.favorites, v)
		case "w":
			pr.WatchLater[v] = true
		case "h":
			pr.Seen[v] = true
			if len(sig.history) < 60 {
				sig.history = append(sig.history, v)
			}
		}
	}
	rows.Close()
	hidden, err := p.s.blockedFilter(r, user)
	if err != nil {
		return nil, nil, err
	}
	pr.Hidden = hidden
	return pr, sig, nil
}

func tag(vs []youtube.VideoItem, src recommend.Source, svc int) []recommend.Tagged {
	out := make([]recommend.Tagged, 0, len(vs))
	for _, v := range vs {
		if recommend.ServiceID(v.URL) == svc {
			out = append(out, recommend.Tagged{Video: v, Source: src})
		}
	}
	return out
}

// subscriptionVideos is the user's current feed snapshot; full waits up to ten seconds for a first build, which
// only ever runs in the background.
func (p *recPools) subscriptionVideos(user string, limit int, full bool) []youtube.VideoItem {
	f := p.s.feeds
	f.mu.Lock()
	cur := f.current[user]
	f.mu.Unlock()
	if cur == nil && full {
		select {
		case <-f.refresh(user):
		case <-time.After(10 * time.Second):
		}
		f.mu.Lock()
		cur = f.current[user]
		f.mu.Unlock()
	}
	if cur == nil {
		return nil
	}
	return cur.videos[:min(limit, len(cur.videos))]
}

func (p *recPools) related(ctx context.Context, seeds []string, limit int, src recommend.Source) []recommend.Tagged {
	var uniq []string
	seen := map[string]bool{}
	for _, s := range seeds {
		if s != "" && !seen[s] && len(uniq) < limit {
			seen[s] = true
			uniq = append(uniq, s)
		}
	}
	out := make([][]recommend.Tagged, len(uniq))
	var wg sync.WaitGroup
	for i, seed := range uniq {
		wg.Go(func() {
			id := store.VideoID(seed)
			if id == "" {
				return
			}
			rel, err := p.s.YT.Related(ctx, id)
			if err != nil {
				return
			}
			for _, v := range rel {
				if v.URL != "" && v.URL != seed && len(out[i]) < 10 {
					out[i] = append(out[i], recommend.Tagged{Video: v, Source: src})
				}
			}
		})
	}
	wg.Wait()
	var all []recommend.Tagged
	for _, o := range out {
		all = append(all, o...)
	}
	return all
}

func (p *recPools) build(ctx context.Context, pr *recommend.Profile, sig *signals, user string, svc int, intent recommend.Intent, full, shorts bool) recommend.Pool {
	if shorts {
		return p.buildShorts(ctx, pr, sig, user, svc, intent, full)
	}
	if !full {
		subs := tag(p.subscriptionVideos(user, 60, false), recommend.Subscription, svc)
		var disc []recommend.Tagged
		if len(subs) == 0 {
			disc = tag(p.trending(ctx, svc), recommend.Trending, svc)
		}
		return recommend.BuildPool(pr, subs, disc, intent, time.Now())
	}
	subs := tag(p.subscriptionVideos(user, 240, true), recommend.Subscription, svc)
	seeds := make([]string, len(subs))
	for i, t := range subs {
		seeds[i] = t.Video.URL
	}
	var favSeeds []string
	for _, f := range sig.favorites {
		if recommend.ServiceID(f) == svc {
			favSeeds = append(favSeeds, f)
		}
	}
	cands := append(p.related(ctx, seeds, 8, recommend.Theme), p.related(ctx, favSeeds, 6, recommend.Exploration)...)
	cands = append(cands, tag(p.trending(ctx, svc), recommend.Trending, svc)...)
	var disc []recommend.Tagged
	for _, t := range recommend.AssembleDiscovery(pr, cands, 48) {
		if recommend.ServiceID(t.Video.URL) == svc {
			disc = append(disc, t)
		}
	}
	return recommend.BuildPool(pr, subs, disc, intent, time.Now())
}

func (p *recPools) resolve(r *http.Request, user string, svc int, intent recommend.Intent, shorts bool) (*recommend.Pool, error) {
	key := poolKey(user, svc, shorts)
	if pool, ok := p.read(r.Context(), key); ok {
		return pool, nil
	}
	pr, sig, err := p.profile(r, user)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	start := !p.building[key]
	p.building[key] = true
	p.mu.Unlock()
	if start {
		go func() {
			defer func() {
				p.mu.Lock()
				delete(p.building, key)
				p.mu.Unlock()
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			pool := p.build(ctx, pr, sig, user, svc, intent, true, shorts)
			b, _ := json.Marshal(pool)
			p.s.Cache.Set(ctx, key, string(b), poolTTL)
			p.s.Cache.Set(ctx, key+":stale", string(b), poolStaleTTL)
		}()
	}
	if pool, ok := p.read(r.Context(), key+":stale"); ok {
		return pool, nil
	}
	pool := p.build(r.Context(), pr, sig, user, svc, intent, false, shorts)
	return &pool, nil
}

func (p *recPools) invalidate(ctx context.Context, user string) {
	for _, svc := range []int{0, 5, 6} {
		for _, shorts := range []bool{false, true} {
			k := poolKey(user, svc, shorts)
			p.s.Cache.Delete(ctx, k)
			p.s.Cache.Delete(ctx, k+":stale")
		}
	}
}
