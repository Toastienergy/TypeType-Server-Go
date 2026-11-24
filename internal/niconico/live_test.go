package niconico

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveWatch(t *testing.T) {
	if os.Getenv("NICONICO_LIVE") == "" {
		t.Skip("set NICONICO_LIVE to call NicoNico")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	start := time.Now()
	v, err := NewClient(nil).Watch(ctx, "sm9")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s by %s (%d s) in %v bid=%v", v.Title, v.OwnerName, v.Duration, time.Since(start), v.Bid != "")
	for _, x := range v.Videos {
		t.Logf("  v %s %.70s", x.Resolution, x.URL)
	}
	for _, x := range v.Audios {
		t.Logf("  a %d %.70s", x.Kbps, x.URL)
	}
}
