package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/config"
	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

const feedPreparingRetryMs = 1000

type feedSnapshot struct {
	generation  int64
	generatedAt int64
	videos      []youtube.VideoItem
	sources     map[string][]string
}

type channelVideos struct {
	videos []youtube.VideoItem
	ok     bool
	at     time.Time
}

// feeds builds each user's subscription feed from the first page of every subscribed channel (and its live tab),
// keeps the last two generations so open cursors stay valid, and rebuilds in the background for users seen in the
// last active window once the feed is older than the freshness setting.
type feeds struct {
	s         *Server
	mu        sync.Mutex
	current   map[string]*feedSnapshot
	previous  map[string]*feedSnapshot
	building  map[string]chan struct{}
	again     map[string]bool
	touched   map[string]time.Time
	channels  sync.Map
	gen       int64
	slots     chan struct{}
	freshness time.Duration
	activeWin time.Duration
}

func newFeeds(s *Server) *feeds {
	f := &feeds{s: s, current: map[string]*feedSnapshot{}, previous: map[string]*feedSnapshot{}, building: map[string]chan struct{}{}, again: map[string]bool{},
		touched: map[string]time.Time{}, slots: make(chan struct{}, max(1, min(config.IntEnv("SUBSCRIPTION_FEED_MAX_CONCURRENT_FETCHES", 20), 20))),
		freshness: time.Duration(max(300, min(config.IntEnv("SUBSCRIPTION_FEED_FRESHNESS_SECONDS", 720), 86400))) * time.Second,
		activeWin: time.Duration(max(300, min(config.IntEnv("SUBSCRIPTION_FEED_ACTIVE_WINDOW_SECONDS", 3600), 7*86400))) * time.Second}
	go f.loop()
	return f
}

func feedKey(v youtube.VideoItem) string {
	if v.URL != "" {
		return v.URL
	}
	if v.ID != "" {
		return v.ID
	}
	return v.UploaderURL + "|" + v.Title
}

func (f *feeds) loop() {
	for range time.Tick(time.Minute) {
		f.mu.Lock()
		var due []string
		for u, at := range f.touched {
			if time.Since(at) > f.activeWin {
				delete(f.touched, u)
				continue
			}
			if c := f.current[u]; c == nil || time.Since(time.UnixMilli(c.generatedAt)) > f.freshness {
				due = append(due, u)
			}
		}
		f.mu.Unlock()
		for _, u := range due {
			f.refresh(u)
		}
	}
}

// refresh starts a rebuild; asked again while one runs (a subscription added mid-build), it runs one more after.
func (f *feeds) refresh(user string) chan struct{} {
	f.mu.Lock()
	if ch, ok := f.building[user]; ok {
		f.again[user] = true
		f.mu.Unlock()
		return ch
	}
	ch := make(chan struct{})
	f.building[user] = ch
	prev := f.current[user]
	f.mu.Unlock()
	go func() {
		defer func() {
			f.mu.Lock()
			delete(f.building, user)
			rerun := f.again[user]
			delete(f.again, user)
			f.mu.Unlock()
			close(ch)
			if rerun {
				f.refresh(user)
			}
		}()
		snap := f.build(context.Background(), user, prev)
		if snap == nil {
			return
		}
		f.mu.Lock()
		f.gen++
		snap.generation = f.gen
		if prev != nil {
			f.previous[user] = prev
		}
		f.current[user] = snap
		f.mu.Unlock()
	}()
	return ch
}

func (f *feeds) invalidate(user string) {
	f.mu.Lock()
	if c := f.current[user]; c != nil {
		c.generatedAt = 0
	}
	f.mu.Unlock()
	f.refresh(user)
}

func (f *feeds) fetch(ctx context.Context, url string) channelVideos {
	if v, ok := f.channels.Load(url); ok {
		cv := v.(channelVideos)
		if cv.ok && time.Since(cv.at) < f.freshness {
			return cv
		}
	}
	f.slots <- struct{}{}
	defer func() { <-f.slots }()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ch, err := f.s.YT.Channel(cctx, url, "")
	cv := channelVideos{ok: err == nil, at: time.Now()}
	if err == nil {
		cv.videos = ch.Videos
		f.channels.Store(url, cv)
	}
	return cv
}

func (f *feeds) build(ctx context.Context, user string, prev *feedSnapshot) *feedSnapshot {
	rows, err := f.s.DB.Query(ctx, "SELECT channel_url FROM subscriptions WHERE user_id = $1", user)
	if err != nil {
		return nil
	}
	var urls []string
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			urls = append(urls, u)
		}
	}
	rows.Close()
	carried := map[string][]youtube.VideoItem{}
	if prev != nil {
		for _, v := range prev.videos {
			for _, src := range prev.sources[feedKey(v)] {
				carried[src] = append(carried[src], v)
			}
		}
	}
	type outcome struct {
		url    string
		videos []youtube.VideoItem
	}
	out := make(chan outcome, len(urls))
	var wg sync.WaitGroup
	for _, u := range urls {
		if !strings.Contains(u, "youtube.com") {
			out <- outcome{u, carried[u]}
			continue
		}
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			var main, live channelVideos
			var inner sync.WaitGroup
			inner.Add(2)
			go func() { defer inner.Done(); main = f.fetch(ctx, u) }()
			go func() { defer inner.Done(); live = f.fetch(ctx, strings.TrimRight(u, "/")+"/streams") }()
			inner.Wait()
			var vids []youtube.VideoItem
			for _, v := range main.videos {
				if !v.IsLive {
					vids = append(vids, v)
				}
			}
			for _, v := range live.videos {
				v.IsLiveContent = true
				vids = append(vids, v)
			}
			if !main.ok || len(main.videos) == 0 || !live.ok {
				seen := map[string]bool{}
				for _, v := range vids {
					seen[feedKey(v)] = true
				}
				for _, v := range carried[u] {
					if !seen[feedKey(v)] && (v.IsLiveContent && !live.ok || !v.IsLiveContent && (!main.ok || len(main.videos) == 0)) {
						vids = append(vids, v)
					}
				}
			}
			out <- outcome{u, vids}
		}(u)
	}
	wg.Wait()
	close(out)
	byKey := map[string]youtube.VideoItem{}
	order := []string{}
	sources := map[string][]string{}
	for o := range out {
		fallback := db.CanonicalChannelURL(o.url)
		for _, v := range o.videos {
			if v.UploaderURL == "" {
				v.UploaderURL = fallback
			}
			k := feedKey(v)
			cur, ok := byKey[k]
			if !ok {
				order = append(order, k)
			}
			if !ok || v.IsLiveContent && !cur.IsLiveContent {
				byKey[k] = v
			}
			if !contains(sources[k], o.url) {
				sources[k] = append(sources[k], o.url)
			}
		}
	}
	now := time.Now().UnixMilli()
	videos := make([]youtube.VideoItem, 0, len(order))
	for _, k := range order {
		videos = append(videos, byKey[k])
	}
	stamp := func(v youtube.VideoItem) int64 {
		if v.IsLive {
			return now
		}
		if v.Uploaded >= 0 {
			return v.Uploaded
		}
		if v.PublishedAt != nil && *v.PublishedAt >= 0 {
			return *v.PublishedAt
		}
		return -1 << 62
	}
	sort.SliceStable(videos, func(i, j int) bool {
		a, b := stamp(videos[i]), stamp(videos[j])
		if a != b {
			return a > b
		}
		return feedKey(videos[i]) < feedKey(videos[j])
	})
	return &feedSnapshot{generatedAt: now, videos: videos, sources: sources}
}

type feedCursor struct {
	Generation int64  `json:"g"`
	Offset     int    `json:"o"`
	Limit      int    `json:"l"`
	HideLive   bool   `json:"h"`
	HideMember bool   `json:"m"`
	Selection  string `json:"s"`
	Oldest     bool   `json:"r"`
}

func (s *Server) feedRoutes(rt *Router) {
	rt.Handle("GET", "/subscriptions/feed", s.authed(s.subscriptionFeed))
}

func (s *Server) subscriptionFeed(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	group := q.Get("groupId")
	if q.Has("groupId") && strings.TrimSpace(group) == "" || q.Has("ungrouped") && q.Get("ungrouped") != "true" && q.Get("ungrouped") != "false" ||
		group != "" && q.Get("ungrouped") == "true" {
		plain(w, r, http.StatusBadRequest, "Invalid subscription filter")
		return
	}
	ungrouped := q.Get("ungrouped") == "true"
	cursorRaw := q.Get("cursor")
	if cursorRaw == "" && group != "" {
		var ok bool
		s.DB.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM subscription_groups WHERE user_id = $1 AND id = $2)", user, group).Scan(&ok)
		if !ok {
			httpx.Error(w, r, http.StatusNotFound, "Subscription group not found", "subscription_group_not_found")
			return
		}
	}
	page, limit := 0, 30
	if n, err := strconv.Atoi(q.Get("page")); err == nil {
		page = max(0, min(n, 10000))
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		limit = max(1, min(n, 100))
	}
	oldest := false
	switch q.Get("order") {
	case "", "newest":
	case "oldest":
		oldest = true
	default:
		plain(w, r, http.StatusBadRequest, "Invalid feed order")
		return
	}
	st, err := s.settings(r, user)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	selKey := "all"
	if group != "" {
		selKey = "group:" + group
	} else if ungrouped {
		selKey = "ungrouped"
	}
	w.Header().Set("Cache-Control", "no-store")
	f := s.feeds
	f.mu.Lock()
	f.touched[user] = time.Now()
	cur, prev := f.current[user], f.previous[user]
	_, building := f.building[user]
	f.mu.Unlock()
	if cur == nil {
		f.refresh(user)
		w.Header().Set("Retry-After", "1")
		httpx.JSON(w, http.StatusAccepted, map[string]any{"code": "subscription_feed_preparing", "retryAfterMs": feedPreparingRetryMs})
		return
	}
	if time.Since(time.UnixMilli(cur.generatedAt)) > f.freshness {
		f.refresh(user)
		building = true
	}
	snap, offset := cur, page*limit
	if cursorRaw != "" {
		var c feedCursor
		b, err := base64.RawURLEncoding.DecodeString(cursorRaw)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Limit != limit || c.HideLive != st.HideSubscriptionLiveStreams ||
			c.HideMember != st.HideMembersOnlyContent || c.Selection != selKey || c.Oldest != oldest {
			httpx.Error(w, r, http.StatusBadRequest, "Invalid subscription feed cursor", "subscription_feed_invalid_cursor")
			return
		}
		switch {
		case c.Generation == cur.generation:
		case prev != nil && c.Generation == prev.generation:
			snap = prev
		default:
			httpx.Error(w, r, http.StatusConflict, "Subscription feed generation is no longer available", "subscription_feed_stale_generation")
			return
		}
		offset = c.Offset
	}
	allowed, err := s.selectionChannels(r, user, group, ungrouped)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	blocked, err := s.blockedFilter(r, user)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	focus := s.focusFor(r.Context(), user)
	var picked []youtube.VideoItem
	canon := map[string]string{}
	for _, v := range snap.videos {
		if st.HideSubscriptionLiveStreams && (v.IsLive || v.IsLiveContent && v.Duration < 0) || st.HideMembersOnlyContent && v.RequiresMembership {
			continue
		}
		if allowed != nil && !anyIn(snap.sources[feedKey(v)], allowed, canon) {
			continue
		}
		if blocked(v) || focus != nil && !focus.keepsFeedVideo(v) {
			continue
		}
		picked = append(picked, v)
	}
	if oldest {
		for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
			picked[i], picked[j] = picked[j], picked[i]
		}
	}
	end := min(offset+limit, len(picked))
	videos := []youtube.VideoItem{}
	if offset < len(picked) {
		videos = picked[offset:end]
	}
	var next *string
	if end < len(picked) {
		b, _ := json.Marshal(feedCursor{snap.generation, end, limit, st.HideSubscriptionLiveStreams, st.HideMembersOnlyContent, selKey, oldest})
		n := base64.RawURLEncoding.EncodeToString(b)
		next = &n
	}
	gen, at := snap.generation, snap.generatedAt
	httpx.JSON(w, http.StatusOK, map[string]any{"videos": videos, "nextpage": next, "generation": gen, "generatedAt": at, "refreshing": building})
}

// anyIn reports whether one of the source URLs is allowed. canon remembers each source's canonical form for the
// request: a feed holds hundreds of videos but only as many sources as subscriptions, and parsing each URL again
// took half of a notifications request.
func anyIn(urls []string, allowed map[string]bool, canon map[string]string) bool {
	for _, u := range urls {
		c, ok := canon[u]
		if !ok {
			c = db.CanonicalChannelURL(u)
			canon[u] = c
		}
		if allowed[c] {
			return true
		}
	}
	return false
}

// selectionChannels is the set of channels a group (or the ungrouped selection) covers; nil means every channel.
func (s *Server) selectionChannels(r *http.Request, user, group string, ungrouped bool) (map[string]bool, error) {
	if group == "" && !ungrouped {
		return nil, nil
	}
	items, err := s.subscriptions(r, user, group, ungrouped, false)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, it := range items {
		out[db.CanonicalChannelURL(it.ChannelURL)] = true
	}
	return out, nil
}
