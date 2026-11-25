package niconico

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const channelFeed = `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:nicoch="https://ch.nicovideo.jp/"><channel><title>t</title>
<item><title>First</title><link>https://www.nicovideo.jp/watch/1577357705</link>
<description><![CDATA[<p class="nico-thumbnail"><img alt="a" src="https://nicovideo.cdn.nimg.jp/thumbnails/1/1"/></p>
<p class="nico-info"><small><strong class="nico-info-length">1:02:03</strong>|<strong class="nico-info-date">2019年12月26日 19：47：37</strong></small></p>]]></description>
<nicoch:isPremium>true</nicoch:isPremium></item>
<item><title>Not a video</title><link>https://example.com/x</link><description></description></item>
</channel></rss>`

const channelPage = `<html><head><title> Weather </title><meta name="description" content="About &amp; more">
<meta property="og:image" content="https://img/icon.jpg"></head><body><script>var channel_id = 'ch1072';</script></body></html>`

func TestChannelReadsFeedAndPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/w/video" && r.URL.Query().Get("page") == "2":
			w.Write([]byte(channelFeed))
		case r.URL.Path == "/w":
			w.Write([]byte(channelPage))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	info, videos, err := NewClient(nil).Channel(context.Background(), srv.URL+"/w", 2)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Weather" || info.Description != "About & more" || info.AvatarURL != "https://img/icon.jpg" || info.ID != "ch1072" {
		t.Fatalf("info %+v", info)
	}
	if len(videos) != 1 {
		t.Fatalf("%d videos", len(videos))
	}
	v := videos[0]
	if v.URL != "https://www.nicovideo.jp/watch/1577357705" || v.Duration != 3723 || v.ViewCount != -1 || !v.Premium ||
		v.ThumbnailURL != "https://nicovideo.cdn.nimg.jp/thumbnails/1/1" || v.Uploaded != 1577357257000 {
		t.Fatalf("video %+v", v)
	}
}

func TestClockSeconds(t *testing.T) {
	for in, want := range map[string]int64{"55": 55, "0:55": 55, "1:16": 76, "2:00:01": 7201} {
		if got := clockSeconds(in); got != want {
			t.Fatalf("%q: %d", in, got)
		}
	}
}
