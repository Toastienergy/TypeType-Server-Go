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

// Bilibili's search filters as PipePipe numbers them; "|2|channels" is what clients send for channels.
var bilibiliSearchTypes = map[string]string{"|0|videos": "video", "|1|lives": "live_room", "|2|channels": "bili_user",
	"|3|animes": "media_bangumi", "|4|movies_and_tv": "media_ft"}

type biliPage struct {
	Q    string `json:"q"`
	Type string `json:"t"`
	Page int    `json:"p"`
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

func (s *Server) bilibiliSearch(ctx context.Context, w http.ResponseWriter, r *http.Request, q, filter, next string) (youtube.SearchPage, bool) {
	out := youtube.SearchPage{Items: []youtube.VideoItem{}, Playlists: []youtube.PlaylistResult{}, Channels: []youtube.ChannelResult{}}
	pg := biliPage{Q: q, Type: "video", Page: 1}
	aggregate := next == "" && filter == ""
	if next != "" {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(next, "bili1."))
		if !strings.HasPrefix(next, "bili1.") || err != nil || json.Unmarshal(b, &pg) != nil || pg.Page < 2 {
			plain(w, r, http.StatusBadRequest, "Invalid page")
			return out, false
		}
	} else if filter != "" {
		t, ok := bilibiliSearchTypes[filter]
		if !ok {
			plain(w, r, http.StatusBadRequest, "Unknown content filter")
			return out, false
		}
		pg.Type = t
	}
	chc := make(chan []map[string]any, 1)
	if aggregate {
		go func() {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			res, _ := s.bili.Search(cctx, q, "bili_user", 1)
			chc <- res
		}()
	}
	res, err := s.bili.Search(ctx, pg.Q, pg.Type, pg.Page)
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
		}
	}
	if len(res) > 0 {
		b, _ := json.Marshal(biliPage{Q: pg.Q, Type: pg.Type, Page: pg.Page + 1})
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
