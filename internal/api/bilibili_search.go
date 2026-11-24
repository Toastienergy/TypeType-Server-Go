package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

type biliPage struct {
	Q     string `json:"q"`
	Type  string `json:"t"`
	Page  int    `json:"p"`
	Extra string `json:"x,omitempty"`
}

func biliDuration(s string) int64 {
	var total int64
	for _, part := range strings.Split(s, ":") {
		n, _ := strconv.ParseInt(part, 10, 64)
		total = total*60 + n
	}
	return total
}

func biliSearchVideo(o map[string]any) youtube.VideoItem {
	s := func(k string) string { v, _ := o[k].(string); return v }
	n := func(k string) int64 { f, _ := o[k].(float64); return int64(f) }
	title := html.UnescapeString(strings.NewReplacer(`<em class="keyword">`, "", "</em>", "").Replace(s("title")))
	bv := s("bvid")
	it := youtube.VideoItem{ID: bv, Title: title, URL: "https://www.bilibili.com/video/" + bv, ThumbnailURL: "https:" + s("pic"),
		UploaderName: s("author"), UploaderAvatarURL: https(s("upic")), Duration: biliDuration(s("duration")), ViewCount: n("play"),
		Uploaded: -1, StreamType: "video_stream"}
	if mid := n("mid"); mid > 0 {
		it.UploaderURL = spaceURL(mid)
	}
	if pd := n("pubdate"); pd > 0 {
		it.Uploaded = pd * 1000
		it.UploadDate = time.Unix(pd, 0).UTC().Format("2006-01-02 15:04:05")
		p := it.Uploaded
		it.PublishedAt = &p
	}
	return it
}

func biliSearchChannel(o map[string]any) youtube.ChannelResult {
	s := func(k string) string { v, _ := o[k].(string); return v }
	n := func(k string) int64 { f, _ := o[k].(float64); return int64(f) }
	c := youtube.ChannelResult{ID: spaceURL(n("mid")), Name: s("uname"), URL: spaceURL(n("mid")), ThumbnailURL: "https:" + s("upic"),
		Description: s("usign"), SubscriberCount: n("fans"), StreamCount: n("videos")}
	for _, k := range []string{"official_verify", "Official"} {
		if v, ok := o[k].(map[string]any); ok {
			if t, ok := v["type"].(float64); ok && (t == 0 || t == 1) {
				label := "personal"
				if t == 1 {
					label = "organization"
				}
				c.IsVerified, c.Verification = true, &label
				break
			}
		}
	}
	return c
}

// bilibiliSearch runs one page; sel is the resolved filter selection, aggregate adds the best channel to a first
// page asked without a content filter, like BilibiliAggregatedSearchService.
func (s *Server) bilibiliSearch(ctx context.Context, w http.ResponseWriter, r *http.Request, q string, sel searchSelection, next string) (youtube.SearchPage, bool) {
	out := youtube.SearchPage{Items: []youtube.VideoItem{}, Playlists: []youtube.PlaylistResult{}, Channels: []youtube.ChannelResult{}}
	pg := biliPage{Q: q, Type: strings.TrimPrefix(sel.content.query, "search_type="), Page: 1, Extra: sel.extra()}
	aggregate := next == "" && !sel.contentGiven
	if next != "" {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(next, "bili1."))
		if !strings.HasPrefix(next, "bili1.") || err != nil || json.Unmarshal(b, &pg) != nil || pg.Page < 2 {
			plain(w, r, http.StatusBadRequest, "Invalid page")
			return out, false
		}
	}
	chc := make(chan []map[string]any, 1)
	if aggregate {
		go func() {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			res, _ := s.bili.Search(cctx, q, "bili_user", "", 1)
			chc <- res
		}()
	}
	res, err := s.bili.Search(ctx, pg.Q, pg.Type, pg.Extra, pg.Page)
	if err != nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Search failed", "error")
		return out, false
	}
	for _, o := range res {
		switch t, _ := o["type"].(string); t {
		case "video":
			out.Items = append(out.Items, biliSearchVideo(o))
		case "bili_user":
			out.Channels = append(out.Channels, biliSearchChannel(o))
		case "live_room":
			out.Items = append(out.Items, biliSearchLive(o))
		case "media_bangumi", "media_ft":
			out.Items = append(out.Items, biliSearchPremium(o))
		}
	}
	if len(res) > 0 {
		b, _ := json.Marshal(biliPage{Q: pg.Q, Type: pg.Type, Page: pg.Page + 1, Extra: pg.Extra})
		n := "bili1." + base64.RawURLEncoding.EncodeToString(b)
		out.NextPage = &n
	}
	if aggregate {
		for _, o := range <-chc {
			out.Channels = append(out.Channels, biliSearchChannel(o))
			break
		}
	}
	return out, true
}

var shanghai = time.FixedZone("UTC+8", 8*3600)

func stripKeyword(s string) string {
	return html.UnescapeString(strings.NewReplacer(`<em class="keyword">`, "", "</em>", "").Replace(s))
}

// biliSearchLive is BilibiliLiveInfoItemExtractor for a live_room result.
func biliSearchLive(o map[string]any) youtube.VideoItem {
	s := func(k string) string { v, _ := o[k].(string); return v }
	n := func(k string) int64 { f, _ := o[k].(float64); return int64(f) }
	title := stripKeyword(s("title"))
	if n("live_status") == 2 {
		title = s("uname") + "的投稿视频轮播"
	}
	u := "https://live.bilibili.com/" + strconv.FormatInt(n("roomid"), 10)
	it := youtube.VideoItem{ID: u, Title: title, URL: u, ThumbnailURL: "https:" + s("user_cover"), UploaderName: s("uname"),
		UploaderAvatarURL: "https:" + s("uface"), Duration: -1, ViewCount: n("online"), Uploaded: -1, StreamType: "live_stream",
		IsLive: true, IsLiveContent: true, UploadDate: s("live_time")}
	if uid := n("uid"); uid > 0 {
		it.UploaderURL = spaceURL(uid)
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s("live_time"), shanghai); err == nil {
		it.Uploaded = t.UnixMilli()
		p := it.Uploaded
		it.PublishedAt = &p
	}
	return it
}

// biliSearchPremium is BilibiliPremiumContentInfoItemExtractor for media_bangumi and media_ft results. PipePipe
// reads pubtime, in seconds, as milliseconds and dates every result in January 1970; this keeps the real date.
func biliSearchPremium(o map[string]any) youtube.VideoItem {
	s := func(k string) string { v, _ := o[k].(string); return v }
	n := func(k string) int64 { f, _ := o[k].(float64); return int64(f) }
	title := s("share_copy")
	if title == "" {
		title = stripKeyword(s("title"))
	}
	u := s("url")
	if u == "" {
		u = s("share_url")
	}
	name := "BiliBili"
	if org := s("org_title"); org != "" {
		name = stripKeyword(org)
	}
	it := youtube.VideoItem{ID: u, Title: title, URL: u, ThumbnailURL: strings.Replace(s("cover"), "http:", "https:", 1), UploaderName: name,
		Duration: n("duration") / 1000, ViewCount: -1, Uploaded: -1, StreamType: "video_stream", UploaderVerified: true}
	sec := n("pubtime")
	if sec == 0 {
		sec = n("pub_time")
	}
	if sec > 0 {
		t := time.Unix(sec, 0).In(shanghai)
		it.UploadDate, it.Uploaded = t.Format("2006-01-02 15:04:05"), t.UnixMilli()
		p := it.Uploaded
		it.PublishedAt = &p
	}
	return it
}
