package recommend

import (
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"typetype.dev/server/internal/youtube"
)

type Video = youtube.VideoItem

type Source int

const (
	Subscription Source = iota
	Trending
	Theme
	Exploration
)

type Intent int

const (
	IntentAuto Intent = iota
	IntentQuick
	IntentDeep
)

func ParseIntent(raw string) Intent {
	switch strings.ToLower(raw) {
	case "quick", "news", "rapid":
		return IntentQuick
	case "deep", "long", "focus":
		return IntentDeep
	}
	return IntentAuto
}

type Tagged struct {
	Video  Video
	Source Source
}

// Profile holds what HomeRecommendationUserSignalService loads; personalization stays off as in the Kotlin server.
// Hidden is the user's blocked-content filter, applied while the pool is built so pages are not left short.
type Profile struct {
	Seen, Subscriptions, Favorites, WatchLater map[string]bool
	Hidden                                     func(Video) bool
}

type Pool struct {
	Subscriptions []Video           `json:"subscriptions"`
	Discovery     []Video           `json:"discovery"`
	SourceByURL   map[string]Source `json:"sourceByUrl"`
}

func recency(uploaded int64, now time.Time) float64 {
	if uploaded <= 0 {
		return 0
	}
	hours := float64(max(0, now.UnixMilli()-uploaded)) / 3_600_000
	return 1 / (1 + hours/60)
}

func score(v Video, p *Profile, base float64, intent Intent, now time.Time) float64 {
	s := base + recency(v.Uploaded, now)
	if p.Subscriptions[v.UploaderURL] {
		s += 0.2
	}
	if p.Favorites[v.URL] {
		s += 0.15
	}
	if p.WatchLater[v.URL] {
		s += 0.08
	}
	if LiveLike(v.Title) {
		s -= 0.5
	}
	switch {
	case intent == IntentQuick && v.Duration >= 1 && v.Duration <= 600:
		s += 0.05
	case intent == IntentDeep && v.Duration >= 1200:
		s += 0.05
	}
	return s
}

type scored struct {
	video  Video
	score  float64
	source Source
}

func scoreAndFilter(cands []Tagged, p *Profile, base float64, intent Intent, now time.Time) []scored {
	order := []string{}
	by := map[string]scored{}
	for _, t := range cands {
		v := t.Video
		if v.URL == "" || p.Seen[v.URL] || p.Hidden != nil && p.Hidden(v) || v.IsLive || LiveLike(v.Title) {
			continue
		}
		sc := scored{v, score(v, p, base, intent, now), t.Source}
		cur, ok := by[v.URL]
		if !ok {
			order = append(order, v.URL)
		}
		if !ok || sc.score > cur.score {
			by[v.URL] = sc
		}
	}
	out := make([]scored, 0, len(order))
	for _, u := range order {
		out = append(out, by[u])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.video.Uploaded != b.video.Uploaded {
			return a.video.Uploaded > b.video.Uploaded
		}
		return a.video.URL < b.video.URL
	})
	return out
}

// AssembleDiscovery is HomeRecommendationDiscoveryAssembler: unsubscribed, not live-like, distinct, capped.
func AssembleDiscovery(p *Profile, cands []Tagged, limit int) []Tagged {
	seen := map[string]bool{}
	var out []Tagged
	for _, t := range cands {
		if len(out) >= limit {
			break
		}
		if p.Subscriptions[t.Video.UploaderURL] || LiveLike(t.Video.Title) || seen[t.Video.URL] {
			continue
		}
		seen[t.Video.URL] = true
		out = append(out, t)
	}
	return out
}

// BuildPool is HomeRecommendationPoolBuilder for the home feed.
func BuildPool(p *Profile, subs, discovery []Tagged, intent Intent, now time.Time) Pool {
	s := scoreAndFilter(subs, p, 1.5, intent, now)
	d := scoreAndFilter(discovery, p, 1.0, intent, now)
	pool := Pool{Subscriptions: []Video{}, Discovery: []Video{}, SourceByURL: map[string]Source{}}
	inSubs := map[string]bool{}
	for _, x := range s {
		inSubs[x.video.URL] = true
		pool.Subscriptions = append(pool.Subscriptions, x.video)
		pool.SourceByURL[x.video.URL] = x.source
	}
	for _, x := range d {
		if inSubs[x.video.URL] {
			continue
		}
		pool.Discovery = append(pool.Discovery, x.video)
		pool.SourceByURL[x.video.URL] = x.source
	}
	return pool
}

// xorwow is kotlin.random.XorWowRandom, so a rotation seed carried in a cursor shuffles the same way on both servers.
type xorwow struct{ x, y, z, w, v, addend int32 }

func newXorWow(seed int64) *xorwow {
	s1, s2 := int32(seed), int32(seed>>32)
	r := &xorwow{x: s1, y: s2, v: ^s1, addend: (s1 << 10) ^ int32(uint32(s2)>>4)}
	for range 64 {
		r.next()
	}
	return r
}

func (r *xorwow) next() int32 {
	t := r.x
	t ^= int32(uint32(t) >> 2)
	r.x, r.y, r.z = r.y, r.z, r.w
	v0 := r.v
	r.w = v0
	t = (t ^ (t << 1)) ^ v0 ^ (v0 << 4)
	r.v = t
	r.addend += 362437
	return t + r.addend
}

func (r *xorwow) intn(n int32) int32 {
	if n&-n == n {
		bits := 0
		for m := n; m > 1; m >>= 1 {
			bits++
		}
		if bits == 0 {
			return 0
		}
		return int32(uint32(r.next()) >> (32 - bits))
	}
	for {
		b := int32(uint32(r.next()) >> 1)
		v := b % n
		if b-v+(n-1) >= 0 {
			return v
		}
	}
}

func rotate(src []Video, seed int64) []Video {
	n := min(len(src), 40)
	if n <= 1 {
		return src
	}
	out := append([]Video(nil), src...)
	rnd := newXorWow(seed)
	for i := n - 1; i >= 1; i-- {
		j := rnd.intn(int32(i + 1))
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Rotate shuffles the first forty entries of each list with the page's seed (HomeRecommendationPoolRotation).
func Rotate(p Pool, seed int64) Pool {
	p.Subscriptions = rotate(p.Subscriptions, seed^0x5EED1234)
	p.Discovery = rotate(p.Discovery, seed^0xD15C0A7)
	return p
}

// NewSeed is a non-zero random rotation seed for a first page.
func NewSeed() int64 {
	for {
		if s := rand.Int64(); s != 0 {
			return s
		}
	}
}
