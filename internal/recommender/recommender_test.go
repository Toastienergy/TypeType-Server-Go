package recommender

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"typetype.dev/server/internal/youtube"
)

type memCache struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemCache() *memCache { return &memCache{m: map[string]string{}} }

func (c *memCache) Get(_ context.Context, k string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok, nil
}

func (c *memCache) Set(_ context.Context, k, v string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = v
	return nil
}

func (c *memCache) Delete(_ context.Context, k string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, k)
	return nil
}

func (c *memCache) Increment(_ context.Context, k string, _ time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, _ := strconv.ParseInt(c.m[k], 10, 64)
	n++
	c.m[k] = strconv.FormatInt(n, 10)
	return n, nil
}

func (c *memCache) DeleteMatching(context.Context, string) (int, error) { return 0, nil }

func testConfig(url string) Config {
	return Config{ServiceURL: url, RequestTimeout: 2 * time.Second, PageDeadline: 2 * time.Second, ParallelBuilds: 2,
		StreamRequestsPerMinute: 15, StreamWallCooldown: 15 * time.Minute, RelatedTTL: time.Hour, SearchTTL: time.Hour,
		ChannelTTL: time.Hour, PageTTL: 30 * time.Minute, SettingsTTL: time.Minute, PageVariants: 3, MaxSourceParallelism: 4}
}

func video(url, channel string) youtube.VideoItem {
	return youtube.VideoItem{URL: url, Title: "t " + url, UploaderURL: channel, UploaderName: "n", Duration: 60, ViewCount: 5, Uploaded: 1000}
}

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{Variant: 2, Offset: 40}
	if c.Encode() != "rec.2.40" {
		t.Fatal(c.Encode())
	}
	if got, ok := DecodeCursor("rec.2.40"); !ok || got != c {
		t.Fatalf("%v %v", got, ok)
	}
	for _, bad := range []string{"", "rec.1", "rec.-1.0", "rec.a.0", "x.1.2", "rec.1.2.3"} {
		if _, ok := DecodeCursor(bad); ok {
			t.Fatalf("%q decoded", bad)
		}
	}
	if !IsCursor("rec.0.0") || IsCursor("abc") {
		t.Fatal("IsCursor")
	}
}

func TestSeedForMatchesKotlin(t *testing.T) {
	p := NewPageStore(newMemCache(), testConfig("x"))
	p.now = func() time.Time { return time.UnixMilli(1_764_000_000_000) }
	window := int64(1_764_000_000_000) / 1_800_000
	if got := p.SeedFor(2); got != int32(window*3+2) {
		t.Fatalf("seed %d", got)
	}
}

func TestHideKeepsTheNewest(t *testing.T) {
	ctx := context.Background()
	p := NewPageStore(newMemCache(), testConfig("x"))
	for i := range maxHidden + 5 {
		p.Hide(ctx, "u", "v"+strconv.Itoa(i), true)
	}
	hidden := p.Hidden(ctx, "u")
	if len(hidden) != maxHidden || hidden["v0"] || !hidden["v504"] {
		t.Fatalf("%d hidden", len(hidden))
	}
	p.Hide(ctx, "u", "v504", false)
	if p.Hidden(ctx, "u")["v504"] {
		t.Fatal("not shown again")
	}
}

func TestCandidateFields(t *testing.T) {
	published := int64(5000)
	v := youtube.VideoItem{URL: "https://www.youtube.com/shorts/abcdefghijk", Title: "a", UploaderURL: "c", UploaderName: "n", Duration: 0,
		ViewCount: -1, Uploaded: 1000, PublishedAt: &published}
	c := Candidate(v)
	if _, ok := c["duration_s"]; ok {
		t.Fatal("duration without a length")
	}
	if _, ok := c["view_count"]; ok {
		t.Fatal("unknown views sent")
	}
	if c["uploaded_at"] != int64(5000) || c["is_short"] != true {
		t.Fatalf("%v", c)
	}
}

func TestSourcesFetchAndRelatedOf(t *testing.T) {
	ctx := context.Background()
	feed := []youtube.VideoItem{video("f1", "c1"), video("f2", "c2"), video("f3", "c3"), video("f4", "c4")}
	var related atomic.Int32
	future := video("later", "c9")
	future.Uploaded = time.Now().Add(time.Hour).UnixMilli()
	load := Loaders{
		Search: func(context.Context, string) []youtube.VideoItem {
			return []youtube.VideoItem{video("s1", "c"), future, video("s2", "c")}
		},
		SubscriptionFeed: func(context.Context, string) []youtube.VideoItem { return feed },
		Related: func(_ context.Context, u string) ([]youtube.VideoItem, bool) {
			related.Add(1)
			return []youtube.VideoItem{video(u, "same"), video("r-"+u, "c"), video("shared", "c")}, false
		},
	}
	cache, m := newMemCache(), &Metrics{}
	cfg := testConfig("x")
	s := NewSources(load, cache, cfg, NewLimiter(cache, func(context.Context) int { return 15 }, cfg.StreamWallCooldown), m)
	plan := []map[string]any{
		{"kind": "search", "query": "go", "limit": float64(5)},
		{"kind": feedKind},
		{"kind": "related", "of": feedKind, "count": float64(2), "among": float64(3)},
	}
	c := s.Fetch(ctx, "u", plan, 7)
	if len(c.Groups) != 3 {
		t.Fatalf("%d groups", len(c.Groups))
	}
	search := c.Groups[0].(map[string]any)["videos"].([]any)
	if len(search) != 2 {
		t.Fatalf("an upcoming video was kept: %v", search)
	}
	rel := c.Groups[2].(map[string]any)["videos"].([]any)
	if related.Load() != 2 || len(rel) != 3 {
		t.Fatalf("%d related calls, %d videos", related.Load(), len(rel))
	}
	again := s.Fetch(ctx, "u", plan, 7)
	if related.Load() != 2 || m.SourceCacheHits.Load() == 0 {
		t.Fatal("related videos not kept")
	}
	if len(again.Groups[2].(map[string]any)["videos"].([]any)) != 3 {
		t.Fatal("the same seed picked other videos")
	}
}

func TestRelatedStopsBehindTheWall(t *testing.T) {
	ctx := context.Background()
	cache, m := newMemCache(), &Metrics{}
	cfg := testConfig("x")
	limiter := NewLimiter(cache, func(context.Context) int { return 15 }, cfg.StreamWallCooldown)
	calls := 0
	s := NewSources(Loaders{Related: func(context.Context, string) ([]youtube.VideoItem, bool) {
		calls++
		return nil, true
	}}, cache, cfg, limiter, m)
	s.Related(ctx, "a")
	if !limiter.Walled(ctx) {
		t.Fatal("a refusal did not raise the wall")
	}
	s.Related(ctx, "b")
	if calls != 1 || m.StreamRequestsRefused.Load() != 1 {
		t.Fatalf("%d calls behind the wall", calls)
	}
}

func TestLimiterCountsEveryOpen(t *testing.T) {
	ctx := context.Background()
	cache := newMemCache()
	l := NewLimiter(cache, func(context.Context) int { return 2 }, time.Minute)
	l.Opened(ctx, false, false)
	if !l.TryAcquire(ctx) {
		t.Fatal("second request refused")
	}
	if l.TryAcquire(ctx) {
		t.Fatal("third request allowed past the limit of 2")
	}
	if l.UsedThisMinute(ctx) != 3 {
		t.Fatalf("used %d", l.UsedThisMinute(ctx))
	}
}

// fakeSignals answers /v1/plan and /v1/rank; the first plan answers 409 when expire is set, as a service that lost
// the history does.
func fakeSignals(t *testing.T, expire bool) (*httptest.Server, *atomic.Int32) {
	var plans atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v1/plan":
			if plans.Add(1) == 1 && expire {
				w.WriteHeader(http.StatusConflict)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"plan_id": "p1", "sources": []any{map[string]any{"kind": "search", "query": "q"}}})
		case "/v1/rank":
			if body["plan_id"] != "p1" || body["trace"] != true {
				t.Errorf("rank body %v", body)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"rows": []any{
					map[string]any{"id": "r1", "title": "Row", "reason": map[string]any{"kind": "search", "ref": "q"},
						"videos": []any{map[string]any{"url": "s1"}, map[string]any{"url": "unknown"}, map[string]any{"url": "s2"}}},
					map[string]any{"id": "r2", "title": "Empty", "videos": []any{map[string]any{"url": "nope"}}},
				},
				"trace": map[string]any{"candidates_received": 2, "rows": []any{map[string]any{"id": "r1", "why": "because"}},
					"dropped": []any{map[string]any{"why": "seen"}, map[string]any{"why": "seen"}},
					"scores":  []any{map[string]any{"url": "s2", "total": 2.5, "factors": map[string]any{"fresh": 1.0}}, map[string]any{"url": "s1", "total": 1.0}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &plans
}

func testBuilder(url string) *Builder {
	cache, m := newMemCache(), &Metrics{}
	cfg := testConfig(url)
	client := NewClient(cfg, nil)
	snaps := NewSnapshots(nil, func(context.Context, string) ([]Subscription, error) { return []Subscription{{"c1", "One"}}, nil },
		func(context.Context, string) ([]string, []string, error) { return nil, nil, nil },
		func(context.Context, string) []string { return nil },
		func(context.Context, string, string) map[string]bool { return map[string]bool{CatSubscriptions: true} })
	sources := NewSources(Loaders{Search: func(context.Context, string) []youtube.VideoItem {
		return []youtube.VideoItem{video("s1", "c"), video("s2", "c")}
	}}, cache, cfg, NewLimiter(cache, func(context.Context) int { return 15 }, time.Minute), m)
	return NewBuilder(client, snaps, sources, m)
}

func TestBuildKeepsLoadedVideosAndReadsTrace(t *testing.T) {
	srv, _ := fakeSignals(t, false)
	page := testBuilder(srv.URL).Build(context.Background(), PageRequest{User: "u", AlgorithmID: "signals", Timeout: time.Second})
	if page == nil {
		t.Fatal("no page")
	}
	if len(page.Rows) != 1 || strings.Join(page.Rows[0].VideoURLs, ",") != "s1,s2" || page.Rows[0].Reason != "search" ||
		page.Rows[0].ReasonRef == nil || *page.Rows[0].ReasonRef != "q" {
		t.Fatalf("rows %+v", page.Rows)
	}
	if len(page.Items) != 2 || page.Algorithm != "signals" {
		t.Fatalf("items %d", len(page.Items))
	}
	tr := page.Trace
	if tr == nil || tr.CandidatesReceived != 2 || tr.Dropped["seen"] != 2 || tr.Shown != 2 || tr.Rows[0].Why != "because" ||
		len(tr.Scores) != 2 || tr.Scores[0].URL != "s2" || tr.Scores[0].Row != "Row" || tr.Sources[0].Detail != "q" {
		t.Fatalf("trace %+v", tr)
	}
}

func TestBuildAsksAgainWhenThePlanExpired(t *testing.T) {
	srv, plans := fakeSignals(t, true)
	page := testBuilder(srv.URL).Build(context.Background(), PageRequest{User: "u", AlgorithmID: "signals", Timeout: time.Second})
	if page == nil || plans.Load() != 2 {
		t.Fatalf("page %v after %d plans", page != nil, plans.Load())
	}
}

func TestHomeBuildsPagesAndHides(t *testing.T) {
	srv, plans := fakeSignals(t, false)
	ctx := context.Background()
	b := testBuilder(srv.URL)
	cache, cfg := newMemCache(), testConfig(srv.URL)
	admin := func(context.Context) (Admin, error) { return Admin{Enabled: true}, nil }
	settings := NewSettingsService(nil, cache, cfg, admin)
	algo := "signals"
	raw, _ := json.Marshal(Settings{Enabled: true, AlgorithmID: &algo})
	cache.Set(ctx, settingsKey("u"), string(raw), time.Minute)
	home := NewHome(cfg, admin, settings, nil, b, NewPageStore(cache, cfg), b.metrics)

	first := home.Get(ctx, "u", 1, "", "en", "UTC")
	if first == nil || len(first.Items) != 1 || first.Items[0].URL != "s1" || !first.HasMore || first.Algorithm != "signals" {
		t.Fatalf("first page %+v", first)
	}
	next := home.Get(ctx, "u", 1, *first.NextCursor, "en", "UTC")
	if next == nil || len(next.Items) != 1 || next.Items[0].URL != "s2" || next.HasMore {
		t.Fatalf("next page %+v", next)
	}
	if plans.Load() != 1 {
		t.Fatalf("the kept page was built again: %d plans", plans.Load())
	}
	home.HideVideo(ctx, "u", "s1", true)
	again := home.Get(ctx, "u", 5, *first.NextCursor, "en", "UTC")
	if again == nil || slices.Contains(again.Rows[0].VideoURLs, "s1") {
		t.Fatalf("hidden video still in the rows: %+v", again)
	}
	if home.LastTrace(ctx, "u") == nil {
		t.Fatal("no trace kept")
	}
}
