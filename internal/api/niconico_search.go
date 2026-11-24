package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

func niconicoSearchItem(o map[string]any) youtube.VideoItem {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	thumb, _ := o["thumbnail"].(map[string]any)
	owner, _ := o["owner"].(map[string]any)
	count, _ := o["count"].(map[string]any)
	dur, _ := o["duration"].(float64)
	views, _ := count["view"].(float64)
	it := youtube.VideoItem{ID: str(o, "id"), Title: str(o, "title"), URL: "https://www.nicovideo.jp/watch/" + str(o, "id"), UploaderName: str(owner, "name"),
		UploaderAvatarURL: str(owner, "iconUrl"), Duration: int64(dur), ViewCount: int64(views), Uploaded: -1, StreamType: "video_stream"}
	for _, k := range []string{"url", "listingUrl", "nHdUrl"} {
		if it.ThumbnailURL = str(thumb, k); it.ThumbnailURL != "" {
			break
		}
	}
	switch str(owner, "ownerType") {
	case "user":
		it.UploaderURL = "https://www.nicovideo.jp/user/" + fmtID(owner["id"])
	case "channel":
		it.UploaderURL = "https://www.nicovideo.jp/channel/" + fmtID(owner["id"])
	}
	if reg := str(o, "registeredAt"); len(reg) >= 16 {
		it.UploadDate = strings.ReplaceAll(reg[:10], "-", "/") + " " + reg[11:16]
		if t, err := time.Parse(time.RFC3339, reg); err == nil {
			it.Uploaded = t.UnixMilli()
			p := it.Uploaded
			it.PublishedAt = &p
		}
	}
	return it
}

func (s *Server) niconicoSearch(ctx context.Context, w http.ResponseWriter, r *http.Request, q string, sel searchSelection, next string) (youtube.SearchPage, bool) {
	out := youtube.SearchPage{Items: []youtube.VideoItem{}, Playlists: []youtube.PlaylistResult{}, Channels: []youtube.ChannelResult{}}
	pg := biliPage{Q: q, Type: "video", Page: 1, Extra: sel.niconicoQuery()}
	switch {
	case next != "":
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(next, "nico1."))
		if !strings.HasPrefix(next, "nico1.") || err != nil || json.Unmarshal(b, &pg) != nil || pg.Page < 2 {
			plain(w, r, http.StatusBadRequest, "Invalid page")
			return out, false
		}
	case sel.content.name != "all":
		notWritten(w, r)
		return out, false
	}
	items, err := s.nico.Search(ctx, pg.Q, pg.Extra, pg.Page)
	if err != nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Search failed", "error")
		return out, false
	}
	for _, o := range items {
		out.Items = append(out.Items, niconicoSearchItem(o))
	}
	if len(items) > 0 {
		b, _ := json.Marshal(biliPage{Q: pg.Q, Type: pg.Type, Page: pg.Page + 1, Extra: pg.Extra})
		n := "nico1." + base64.RawURLEncoding.EncodeToString(b)
		out.NextPage = &n
	}
	return out, true
}
