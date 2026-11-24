package api

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"typetype.dev/server/internal/youtube"
)

func TestRenderRss(t *testing.T) {
	desc := "a <b> & \"c\"\x01"
	pub := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC).UnixMilli()
	feed := &rssFeed{Name: "Tech & <News>"}
	v := youtube.VideoItem{ID: "abc", URL: "https://www.youtube.com/watch?v=abc~1*2 3", Title: "T <1>", UploaderName: "U", ShortDescription: &desc,
		ThumbnailURL: "https://i.ytimg.com/vi/abc/hq.jpg?a=1&b=\"", PublishedAt: &pub}
	out := string(renderRss(feed, []youtube.VideoItem{v}, "https://tt.example", pub))
	var doc struct {
		Channel struct {
			Title     string `xml:"title"`
			LastBuild string `xml:"lastBuildDate"`
			Items     []struct {
				Link  string `xml:"link"`
				GUID  string `xml:"guid"`
				Desc  string `xml:"description"`
				Thumb struct {
					URL string `xml:"url,attr"`
				} `xml:"http://search.yahoo.com/mrss/ thumbnail"`
				PubDate string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	it := doc.Channel.Items[0]
	if doc.Channel.Title != "Tech & <News>" || doc.Channel.LastBuild != "Wed, 4 Mar 2026 05:06:07 GMT" || it.PubDate != doc.Channel.LastBuild {
		t.Fatalf("channel %+v", doc.Channel)
	}
	if it.Link != "https://tt.example/watch?v=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3Dabc%7E1*2+3" || it.GUID != "0:abc" || it.Desc != "a <b> & \"c\"" ||
		it.Thumb.URL != "" {
		t.Fatalf("item %+v", it)
	}
	if !strings.HasPrefix(out, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel><title>Tech &amp; &lt;News&gt;</title>`) {
		t.Fatalf("head %s", out[:200])
	}
	v.ThumbnailURL = "https://i.ytimg.com/vi/abc/hq.jpg?a=1&b=2"
	if out := string(renderRss(feed, []youtube.VideoItem{v}, "https://tt.example", pub)); !strings.Contains(out, `<media:thumbnail url="https://i.ytimg.com/vi/abc/hq.jpg?a=1&amp;b=2"></media:thumbnail>`) {
		t.Fatalf("thumbnail %s", out)
	}
}

func TestRssThrottle(t *testing.T) {
	th := &rssThrottle{windows: map[string]*[2]int64{}}
	if th.acquire("f", 2, 1000) != 0 || th.acquire("f", 2, 2000) != 0 {
		t.Fatal("first two refused")
	}
	if got := th.acquire("f", 2, 31_500); got != 30 {
		t.Fatalf("retry %d", got)
	}
	if th.acquire("f", 2, 61_000) != 0 {
		t.Fatal("new window refused")
	}
}
