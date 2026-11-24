package bilibili

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveVideo(t *testing.T) {
	if os.Getenv("BILIBILI_LIVE") == "" {
		t.Skip("set BILIBILI_LIVE to call Bilibili")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	c := NewClient(nil)
	start := time.Now()
	v, err := c.Video(ctx, "https://www.bilibili.com/video/BV17x411w7KC")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s by %s (%d s, cid %d) in %v: %d video, %d audio", v.Title, v.OwnerName, v.Duration, v.CID, time.Since(start), len(v.VideoOnly), len(v.Audio))
	for _, m := range v.VideoOnly[:min(3, len(v.VideoOnly))] {
		t.Logf("  v %dx%d %s q%d init %d-%d idx %d-%d alts %d %.60s", m.Width, m.Height, m.Codec, m.QualityID, m.InitStart, m.InitEnd, m.IndexStart, m.IndexEnd, len(m.Alternates), m.URL)
	}
	for _, m := range v.Audio {
		t.Logf("  a %s %d q%d", m.Codec, m.Bandwidth, m.QualityID)
	}
}
