package recommend

import (
	"strings"
	"time"

	"typetype.dev/server/internal/store"
)

func IsShort(v Video) bool {
	return v.IsShortFormContent || v.Duration >= 1 && v.Duration <= 85 || strings.Contains(v.URL, "/shorts/")
}

// MarkShorts keeps the related videos short enough to be Shorts and flags them as such.
func MarkShorts(in []Tagged) []Tagged {
	var out []Tagged
	for _, t := range in {
		if t.Video.Duration >= 1 && t.Video.Duration <= 85 {
			t.Video.IsShortFormContent = true
		}
		if t.Video.IsShortFormContent {
			out = append(out, t)
		}
	}
	return out
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	in := set(a)
	inter, union := 0, len(in)
	for x := range set(b) {
		if in[x] {
			inter++
		} else {
			union++
		}
	}
	return float64(inter) / float64(union)
}

// DedupeShorts is HomeRecommendationShortsDeduplicator: drops watched videos, repeated or near-identical titles, and
// more than two Shorts from one subscribed channel.
func DedupeShorts(cands []Tagged, history, subscriptions []string) []Tagged {
	if len(cands) <= 1 {
		return cands
	}
	watched := map[string]bool{}
	for _, h := range history {
		if id := store.VideoID(h); id != "" {
			watched[id] = true
		}
	}
	subs := set(subscriptions)
	delete(subs, "")
	seenKeys := map[string]bool{}
	var seenTokens [][]string
	perChannel := map[string]int{}
	var out []Tagged
	for _, t := range cands {
		v := t.Video
		if id := store.VideoID(v.URL); id != "" && watched[id] {
			continue
		}
		key, tokens := semanticKey(v.Title), Tokenize(v.Title)
		if key != "" && seenKeys[key] {
			continue
		}
		near := false
		for _, prev := range seenTokens {
			if jaccard(prev, tokens) >= 0.60 {
				near = true
				break
			}
		}
		if len(tokens) > 0 && near || subs[v.UploaderURL] && perChannel[v.UploaderURL] >= 2 {
			continue
		}
		if key != "" {
			seenKeys[key] = true
		}
		if len(tokens) > 0 {
			seenTokens = append(seenTokens, tokens)
		}
		perChannel[v.UploaderURL]++
		out = append(out, t)
	}
	return out
}

// ShortsFallback lends subscription Shorts to discovery when discovery is empty and subscriptions are few.
func ShortsFallback(subs, disc []Tagged) []Tagged {
	if len(disc) > 0 || len(subs) >= 6 {
		return disc
	}
	base := subs
	if len(base) > 4 {
		base = base[4:]
	}
	seen := map[string]bool{}
	for _, t := range base[:min(24, len(base))] {
		if !seen[t.Video.URL] {
			seen[t.Video.URL] = true
			disc = append(disc, Tagged{Video: t.Video, Source: Exploration})
		}
	}
	return disc
}

// BuildShortsPool scores like BuildPool but keeps only Shorts.
func BuildShortsPool(p *Profile, subs, discovery []Tagged, intent Intent, now time.Time) Pool {
	keep := func(in []Tagged) []Tagged {
		var out []Tagged
		for _, t := range in {
			if t.Video.IsShortFormContent || t.Video.Duration >= 1 && t.Video.Duration <= 85 {
				out = append(out, t)
			}
		}
		return out
	}
	return BuildPool(p, keep(subs), keep(discovery), intent, now)
}

// Debug is the shorts page breakdown the debug query asks for.
type Debug struct {
	ItemSources     map[string]string `json:"itemSources"`
	SourceBreakdown map[string]int    `json:"sourceBreakdown"`
	DiscoveryRatio  float64           `json:"discoveryRatio"`
	TargetRatio     float64           `json:"targetDiscoveryRatio"`
	FloorRatio      float64           `json:"discoveryFloorRatio"`
}

var sourceLabel = map[Source]string{Subscription: "subs", Trending: "trending", Theme: "theme", Exploration: "exploration"}

func (pg Page) Debug() Debug {
	d := Debug{ItemSources: map[string]string{}, SourceBreakdown: map[string]int{"subs": 0, "trending": 0, "theme": 0, "exploration": 0},
		TargetRatio: pg.TargetRatio, FloorRatio: pg.FloorRatio}
	for _, v := range pg.Items {
		src, ok := pg.Sources[v.URL]
		if !ok {
			src = Exploration
		}
		d.ItemSources[v.URL] = sourceLabel[src]
	}
	for _, l := range d.ItemSources {
		d.SourceBreakdown[l]++
	}
	d.DiscoveryRatio = float64(pg.DiscCount) / float64(max(1, len(pg.Items)))
	return d
}

// MixShorts mixes a Shorts page and, when discovery fell under its floor, lends subscription Shorts to discovery and
// mixes again from a rewound cursor (HomeRecommendationShortsRefresher).
func MixShorts(pool *Pool, c Cursor, limit int, intent Intent, now time.Time) Page {
	page := mix(pool, c, limit, intent, true, now)
	if page.DiscCount >= min(int(float64(len(page.Items))*page.FloorRatio), len(page.Items)) {
		return page
	}
	var recovered []Video
	if len(pool.Subscriptions) > 4 {
		recovered = pool.Subscriptions[4:min(28, len(pool.Subscriptions))]
	}
	rewound := c
	rewound.DiscIndex = max(0, c.DiscIndex-24)
	if len(recovered) == 0 {
		if page.DiscCount > 0 {
			return mix(pool, rewound, limit, intent, true, now)
		}
		return page
	}
	merged := Pool{Subscriptions: pool.Subscriptions, SourceByURL: map[string]Source{}}
	seen := map[string]bool{}
	for _, v := range append(append([]Video(nil), pool.Discovery...), recovered...) {
		if !seen[v.URL] {
			seen[v.URL] = true
			merged.Discovery = append(merged.Discovery, v)
		}
	}
	for k, v := range pool.SourceByURL {
		merged.SourceByURL[k] = v
	}
	for _, v := range recovered {
		merged.SourceByURL[v.URL] = Exploration
	}
	return mix(&merged, rewound, limit, intent, true, now)
}
