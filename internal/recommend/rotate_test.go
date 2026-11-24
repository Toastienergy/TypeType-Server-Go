package recommend

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestXorWowMatchesKotlin(t *testing.T) {
	r := newXorWow(0x1234567890ABCDEF ^ 0x5EED1234)
	var b strings.Builder
	for i := 39; i >= 1; i-- {
		fmt.Fprintf(&b, "%d,", r.intn(int32(i+1)))
	}
	if want := "25,25,12,33,24,20,21,29,14,28,2,11,1,8,5,23,10,20,16,0,4,14,8,0,3,3,12,8,10,9,0,2,7,0,5,4,1,1,1,"; b.String() != want {
		t.Fatalf("got %s", b.String())
	}
	r2 := newXorWow(-7)
	if a, c, d := r2.intn(64), r2.intn(7), r2.intn(1); a != 7 || c != 2 || d != 0 {
		t.Fatalf("got %d %d %d", a, c, d)
	}
}

func TestMixDiversityAndCursor(t *testing.T) {
	now := time.Now()
	var subs, disc []Tagged
	for i := range 30 {
		ch := fmt.Sprintf("https://www.youtube.com/channel/S%d", i%5)
		subs = append(subs, Tagged{Video{URL: fmt.Sprintf("https://www.youtube.com/watch?v=s%02d", i), Title: fmt.Sprintf("alpha%d bravo%d", i, i),
			UploaderURL: ch, Uploaded: now.UnixMilli() - int64(i)*3600_000}, Subscription})
		disc = append(disc, Tagged{Video{URL: fmt.Sprintf("https://www.youtube.com/watch?v=d%02d", i), Title: fmt.Sprintf("charlie%d delta%d", i, i),
			UploaderURL: fmt.Sprintf("https://www.youtube.com/channel/D%d", i)}, Trending})
	}
	p := &Profile{Seen: map[string]bool{}, Subscriptions: map[string]bool{}, Favorites: map[string]bool{}, WatchLater: map[string]bool{}}
	pool := BuildPool(p, subs, disc, IntentAuto, now)
	page := Mix(&pool, NewCursor(), 20, IntentAuto, now)
	if len(page.Items) != 20 || page.NextCursor == "" {
		t.Fatalf("items %d cursor %q", len(page.Items), page.NextCursor)
	}
	per := map[string]int{}
	for i, v := range page.Items {
		per[v.UploaderURL]++
		if i > 0 && page.Items[i-1].UploaderURL == v.UploaderURL {
			t.Fatalf("same channel twice in a row at %d", i)
		}
	}
	for ch, n := range per {
		if n > 2 {
			t.Fatalf("%s shown %d times", ch, n)
		}
	}
	c, ok := DecodeCursor(page.NextCursor)
	if !ok || c.SubIndex+c.DiscIndex < 20 || len(c.RecentURLs) != 20 {
		t.Fatalf("cursor %+v %v", c, ok)
	}
	next := Mix(&pool, c, 20, IntentAuto, now)
	for _, v := range next.Items {
		for _, u := range c.RecentURLs {
			if v.URL == u {
				t.Fatalf("%s repeated on the next page", u)
			}
		}
	}
}
