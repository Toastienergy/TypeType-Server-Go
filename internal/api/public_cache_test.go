package api

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"typetype.dev/server/internal/cache"
)

func testCacheServer(t *testing.T) *Server {
	u := os.Getenv("TEST_DRAGONFLY_URL")
	if u == "" {
		t.Skip("TEST_DRAGONFLY_URL not set")
	}
	c, err := cache.Open(u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return &Server{Cache: c, Log: slog.New(slog.DiscardHandler)}
}

func TestPublicCacheLoadsOnceAndFallsBack(t *testing.T) {
	s := testCacheServer(t)
	ctx := context.Background()
	key := publicCacheKey("test-area", strconv.FormatInt(time.Now().UnixNano(), 10))
	t.Cleanup(func() {
		for _, k := range []string{key, key + ":good", key + ":backoff"} {
			s.Cache.Delete(ctx, k)
		}
	})
	area := cacheArea[[]string]{name: "test", keepLastGood: true, admit: func(v []string) admission {
		return firstPageAdmission("", len(v) == 0, 1)
	}}
	var loads atomic.Int32
	good := func(context.Context) ([]string, *extractFail) {
		loads.Add(1)
		time.Sleep(50 * time.Millisecond)
		return []string{"a"}, nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if v, f := publicCached(s, ctx, area, key, good); f != nil || len(v) != 1 {
				t.Errorf("got %v %v", v, f)
			}
		})
	}
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("%d loads for 20 concurrent callers", loads.Load())
	}
	time.Sleep(1100 * time.Millisecond)
	down := func(context.Context) ([]string, *extractFail) {
		loads.Add(1)
		return nil, &extractFail{Status: 422, Message: "down", Code: "error"}
	}
	if v, f := publicCached(s, ctx, area, key, down); f != nil || len(v) != 1 || v[0] != "a" {
		t.Fatalf("failure not covered by the last good answer: %v %v", v, f)
	}
	before := loads.Load()
	if v, _ := publicCached(s, ctx, area, key, down); len(v) != 1 || loads.Load() != before {
		t.Fatal("backoff did not keep the provider out")
	}
	var b cacheBackoff
	if !readJSON(s, ctx, key+":backoff", &b) || b.Failures != 1 || b.RetryAt-time.Now().UnixMilli() > 30_000 {
		t.Fatalf("backoff %+v", b)
	}
	s.Cache.Delete(ctx, key+":backoff")
	empty := func(context.Context) ([]string, *extractFail) { return []string{}, nil }
	if v, _ := publicCached(s, ctx, area, key, empty); len(v) != 1 {
		t.Fatal("an empty first page replaced the last good answer")
	}
}

func TestStreamCacheTTL(t *testing.T) {
	now := int64(1_800_000_000)
	audio := []audioStream{{URL: "https://rr1.googlevideo.com/videoplayback?expire=" + strconv.FormatInt(now+7200, 10)}}
	cases := []struct {
		r    streamResponse
		want int64
	}{
		{streamResponse{AudioStreams: audio, DislikeCount: -1, SponsorBlockAvailable: true}, 300},
		{streamResponse{AudioStreams: audio, DislikeCount: 3, SponsorBlockAvailable: true}, 6900},
		{streamResponse{AudioStreams: audio, DislikeCount: 3}, 120},
		{streamResponse{AudioStreams: audio, DislikeCount: 3, SponsorBlockAvailable: true, IsLiveContent: true}, 300},
		{streamResponse{AudioStreams: audio, IsLive: true}, 0},
		{streamResponse{SponsorBlockAvailable: true}, 0},
		{streamResponse{AudioStreams: []audioStream{{URL: "https://x/videoplayback%3Fexpire%3D" + strconv.FormatInt(now+200, 10)}}, DislikeCount: 3, SponsorBlockAvailable: true}, 0},
	}
	for i, c := range cases {
		if got := streamCacheTTL(&c.r, now); got != c.want {
			t.Errorf("case %d: %d, want %d", i, got, c.want)
		}
	}
}
