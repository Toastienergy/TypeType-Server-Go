package api

import (
	"encoding/json"
	"testing"
	"time"

	"typetype.dev/server/internal/recommend"
)

func recPoolOf(n int) recommend.Pool {
	feed := feedOf(n).videos
	return recommend.Pool{Subscriptions: feed[:n/2], Discovery: feed[n/2:], SourceByURL: map[string]recommend.Source{}}
}

func TestRecPoolReadDecodesOnlyChangedValues(t *testing.T) {
	s := testCacheServer(t)
	p := &recPools{s: s, building: map[string]bool{}, trend: map[int]trendEntry{}, decoded: map[string]decodedPool{}}
	key := "test:rec-pool-read:" + time.Now().Format(time.RFC3339Nano)
	t.Cleanup(func() { s.Cache.Delete(t.Context(), key) })
	a, _ := json.Marshal(recPoolOf(40))
	s.Cache.Set(t.Context(), key, string(a), time.Minute)
	first, ok := p.read(t.Context(), key)
	second, ok2 := p.read(t.Context(), key)
	if !ok || !ok2 || first != second || len(first.Subscriptions) != 20 {
		t.Fatalf("unchanged value decoded again or lost: %v %v", ok, ok2)
	}
	b, _ := json.Marshal(recPoolOf(60))
	s.Cache.Set(t.Context(), key, string(b), time.Minute)
	if third, ok := p.read(t.Context(), key); !ok || third == first || len(third.Subscriptions) != 30 {
		t.Fatal("changed value not decoded again")
	}
	s.Cache.Delete(t.Context(), key)
	if _, ok := p.read(t.Context(), key); ok {
		t.Fatal("deleted pool still read")
	}
}

func BenchmarkRecPoolDecode(b *testing.B) {
	raw, _ := json.Marshal(recPoolOf(600))
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		var pool recommend.Pool
		json.Unmarshal(raw, &pool)
	}
}
