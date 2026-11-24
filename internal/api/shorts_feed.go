package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/recommend"
	"typetype.dev/server/internal/store"
	"typetype.dev/server/internal/youtube"
)

// The subscription Shorts feed is SubscriptionShortsFeedService: the first page of each subscribed channel's Shorts
// tab, deduplicated, newest first, then dealt round-robin across uploaders. It is stored under the Kotlin key.
func shortsKey(user string) string {
	sum := sha256.Sum256([]byte(user))
	return "feed:shorts:" + hex.EncodeToString(sum[:])
}

func (s *Server) cachedShorts(ctx context.Context, user string) ([]youtube.VideoItem, bool) {
	v, ok, _ := s.Cache.Get(ctx, shortsKey(user))
	if !ok {
		return nil, false
	}
	var out []youtube.VideoItem
	return out, json.Unmarshal([]byte(v), &out) == nil
}

func shortsTab(channel string) string {
	if strings.HasSuffix(channel, "/shorts") {
		return channel
	}
	return strings.TrimSuffix(channel, "/") + "/shorts"
}

func (s *Server) shortsFeed(ctx context.Context, user string) ([]youtube.VideoItem, error) {
	if all, ok := s.cachedShorts(ctx, user); ok {
		return all, nil
	}
	rows, err := s.DB.Query(ctx, "SELECT channel_url FROM subscriptions WHERE user_id = $1", user)
	if err != nil {
		return nil, err
	}
	var channels []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			channels = append(channels, c)
		}
	}
	rows.Close()
	results := make([][]youtube.VideoItem, len(channels))
	failed := make([]bool, len(channels))
	slots := make(chan struct{}, 20)
	var wg sync.WaitGroup
	for i, c := range channels {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			fetch := func(u string) ([]youtube.VideoItem, bool) {
				cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				ch, err := s.YT.Channel(cctx, u, "")
				if err != nil {
					return nil, false
				}
				return ch.Videos, true
			}
			if recommend.ServiceID(c) == 0 {
				if vs, ok := fetch(shortsTab(c)); ok && len(vs) > 0 {
					for j := range vs {
						vs[j].IsShortFormContent = true
					}
					results[i] = vs
					return
				}
			}
			results[i], _ = fetch(c)
			failed[i] = results[i] == nil
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	var all []youtube.VideoItem
	for _, vs := range results {
		for _, v := range vs {
			key := store.VideoID(v.URL)
			if key == "" {
				key = v.URL
			}
			if seen[key] || !v.IsShortFormContent {
				continue
			}
			seen[key] = true
			if id := store.VideoID(v.URL); id != "" && !strings.Contains(strings.ToLower(v.URL), "/shorts/") {
				v.URL = "https://www.youtube.com/shorts/" + id
			}
			all = append(all, v)
		}
	}
	all = diversify(all)
	nFailed := 0
	for _, f := range failed {
		if f {
			nFailed++
		}
	}
	ttl := time.Hour
	switch {
	case nFailed == len(channels) && nFailed > 0:
		ttl = 30 * time.Second
	case nFailed > 0:
		ttl = 5 * time.Minute
	}
	if all == nil {
		all = []youtube.VideoItem{}
	}
	b, _ := json.Marshal(all)
	s.Cache.Set(ctx, shortsKey(user), string(b), ttl)
	return all, nil
}

func uploadedKey(v youtube.VideoItem) int64 {
	if v.Uploaded == -1 {
		return -1 << 63
	}
	return v.Uploaded
}

func diversify(vs []youtube.VideoItem) []youtube.VideoItem {
	sort.SliceStable(vs, func(i, j int) bool { return uploadedKey(vs[i]) > uploadedKey(vs[j]) })
	var order []string
	groups := map[string][]youtube.VideoItem{}
	for _, v := range vs {
		k := v.UploaderURL
		if k == "" {
			k = v.UploaderName
		}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], v)
	}
	out := make([]youtube.VideoItem, 0, len(vs))
	for len(order) > 0 {
		var next []string
		for _, k := range order {
			out = append(out, groups[k][0])
			if groups[k] = groups[k][1:]; len(groups[k]) > 0 {
				next = append(next, k)
			}
		}
		order = next
	}
	return out
}

func pageOf(all []youtube.VideoItem, page, limit int) ([]youtube.VideoItem, *string) {
	from := page * limit
	if from >= len(all) {
		return []youtube.VideoItem{}, nil
	}
	to := min(from+limit, len(all))
	if to < len(all) {
		n := strconv.Itoa(page + 1)
		return all[from:to], &n
	}
	return all[from:to], nil
}

func shortScore(v youtube.VideoItem, sub bool, now int64) float64 {
	s := 0.85
	if sub {
		s = 1
	}
	if v.Uploaded > 0 {
		s += 1 / (1 + float64(max(0, now-v.Uploaded))/3_600_000/24)
	}
	return s
}

// blendShorts is SubscriptionShortsBlendService: at most 60% subscription Shorts, interleaved with trending Shorts.
func (s *Server) blendShorts(ctx context.Context, subs []youtube.VideoItem, page, limit int) ([]youtube.VideoItem, *string) {
	now := time.Now().UnixMilli()
	subKeys := map[string]bool{}
	for _, v := range subs {
		subKeys[store.VideoID(v.URL)] = true
	}
	var disc []youtube.VideoItem
	seen := map[string]bool{}
	for _, v := range s.recs.trending(ctx, 0) {
		if !v.IsShortFormContent && (v.Duration < 1 || v.Duration > 60) {
			continue
		}
		if id := store.VideoID(v.URL); id != "" {
			v.URL = "https://www.youtube.com/shorts/" + id
		}
		if subKeys[store.VideoID(v.URL)] || seen[v.URL] {
			continue
		}
		seen[v.URL] = true
		disc = append(disc, v)
	}
	rs := append([]youtube.VideoItem(nil), subs...)
	sort.SliceStable(rs, func(i, j int) bool { return shortScore(rs[i], true, now) > shortScore(rs[j], true, now) })
	sort.SliceStable(disc, func(i, j int) bool { return shortScore(disc[i], false, now) > shortScore(disc[j], false, now) })
	dpage := disc[min(page*limit, len(disc)):min(page*limit+limit, len(disc))]
	subChannels := map[string]bool{}
	for _, v := range rs {
		if v.UploaderURL != "" {
			subChannels[v.UploaderURL] = true
		}
	}
	quota := 0
	if len(rs) > 0 {
		quota = max(1, int(float64(limit)*0.6))
	}
	var out []youtube.VideoItem
	si, di := 0, 0
	for len(out) < limit && (si < len(rs) || di < len(dpage)) {
		subCount := 0
		for _, v := range out {
			if subChannels[v.UploaderURL] {
				subCount++
			}
		}
		if si < len(rs) && subCount < quota {
			out = append(out, rs[si])
			si++
		}
		if len(out) < limit && di < len(dpage) {
			out = append(out, dpage[di])
			di++
		}
		if si < len(rs) && di >= len(dpage) && len(out) < limit {
			out = append(out, rs[si])
			si++
		}
	}
	uniq := map[string]bool{}
	videos := []youtube.VideoItem{}
	for _, v := range out {
		if !uniq[v.URL] && len(videos) < limit {
			uniq[v.URL] = true
			videos = append(videos, v)
		}
	}
	var next *string
	if len(subs) >= limit || (page+1)*limit < len(disc) {
		n := strconv.Itoa(page + 1)
		next = &n
	}
	return videos, next
}

func (s *Server) shortsFeedRoutes(rt *Router) {
	rt.Handle("GET", "/subscriptions/shorts", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		q := r.URL.Query()
		page, limit := 0, 30
		if n, err := strconv.Atoi(q.Get("page")); err == nil {
			page = max(0, min(n, 10000))
		}
		if n, err := strconv.Atoi(q.Get("limit")); err == nil {
			limit = max(1, min(n, 100))
		}
		all, err := s.shortsFeed(r.Context(), user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		var videos []youtube.VideoItem
		var next *string
		if q.Get("blended") == "false" {
			videos, next = pageOf(all, page, limit)
		} else {
			subs, _ := pageOf(all, page, limit)
			videos, next = s.blendShorts(r.Context(), subs, page, limit)
		}
		hidden, err := s.blockedFilter(r, user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		kept := []youtube.VideoItem{}
		for _, v := range videos {
			if !hidden(v) {
				kept = append(kept, v)
			}
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"videos": kept, "nextpage": next, "generation": nil, "generatedAt": nil, "refreshing": false})
	}))
}
