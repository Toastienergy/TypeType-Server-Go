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
	case sel.content.name == "lives" || sel.content.name == "playlists":
		pg.Type = sel.content.name
		if pg.Type == "playlists" && pg.Extra != "" {
			pg.Extra = "&" + pg.Extra
		}
	}
	nextPage := func() *string {
		b, _ := json.Marshal(biliPage{Q: pg.Q, Type: pg.Type, Page: pg.Page + 1, Extra: pg.Extra})
		n := "nico1." + base64.RawURLEncoding.EncodeToString(b)
		return &n
	}
	switch pg.Type {
	case "lives":
		programs, more, err := s.nico.LiveSearch(ctx, pg.Q, pg.Page)
		if err != nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Search failed", "error")
			return out, false
		}
		for _, o := range programs {
			out.Items = append(out.Items, niconicoLiveItem(o))
		}
		if more {
			out.NextPage = nextPage()
		}
		return out, true
	case "playlists":
		lists, err := s.nico.ListSearch(ctx, pg.Q, pg.Extra, pg.Page)
		if err != nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Search failed", "error")
			return out, false
		}
		for _, o := range lists {
			out.Playlists = append(out.Playlists, niconicoListItem(o))
		}
		if len(lists) > 0 {
			out.NextPage = nextPage()
		}
		return out, true
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
		out.NextPage = nextPage()
	}
	return out, true
}

// niconicoLiveItem is NiconicoLiveSearchInfoItemExtractor: a programme on air, from its supplier (a user) or else its
// social group (a channel), dated in Japan's time as PipePipe prints it.
func niconicoLiveItem(o map[string]any) youtube.VideoItem {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	stats, _ := o["statistics"].(map[string]any)
	supplier, _ := o["supplier"].(map[string]any)
	group, _ := o["socialGroup"].(map[string]any)
	watchers, _ := stats["watchCount"].(float64)
	u := str(o, "watchPageUrl")
	it := youtube.VideoItem{ID: u, Title: str(o, "title"), URL: u, ThumbnailURL: absoluteURL(str(o, "listingThumbnail")), Duration: -1,
		ViewCount: int64(watchers), Uploaded: -1, StreamType: "live_stream", IsLive: true, IsLiveContent: true}
	if d := str(o, "description"); strings.TrimSpace(d) != "" {
		it.ShortDescription = &d
	}
	if supplier != nil {
		icons, _ := supplier["icons"].(map[string]any)
		it.UploaderName, it.UploaderAvatarURL = str(supplier, "name"), str(icons, "uri150x150")
	} else {
		it.UploaderName, it.UploaderAvatarURL = str(group, "name"), str(group, "thumbnailUrl")
	}
	if id := str(supplier, "programProviderId"); id != "" {
		it.UploaderURL = "https://www.nicovideo.jp/user/" + id
	} else if id := str(group, "id"); id != "" {
		it.UploaderURL = "https://ch.nicovideo.jp/" + id
	}
	if begin, _ := o["beginTime"].(float64); begin != 0 {
		t := time.Unix(int64(begin), 0).In(japan)
		it.UploadDate = t.Format("2006-01-02T15:04")
		if t.Second() != 0 {
			it.UploadDate += t.Format(":05")
		}
		it.UploadDate += "+09:00"
		it.Uploaded = t.UnixMilli()
		p := it.Uploaded
		it.PublishedAt = &p
	}
	return it
}

var japan = time.FixedZone("UTC+9", 9*3600)

// niconicoListItem is NiconicoPlaylistInfoItemExtractor: a user's mylist.
func niconicoListItem(o map[string]any) youtube.PlaylistResult {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	owner, _ := o["owner"].(map[string]any)
	name := str(o, "name")
	if name == "" {
		name = str(o, "title")
	}
	items, _ := o["itemsCount"].(float64)
	videos, _ := o["videoCount"].(float64)
	u := "https://www.nicovideo.jp/user/" + fmtID(owner["id"]) + "/mylist/" + fmtID(o["id"])
	return youtube.PlaylistResult{ID: u, Title: name, URL: u, ThumbnailURL: absoluteURL(str(o, "thumbnailUrl")), UploaderName: str(owner, "name"),
		StreamCount: int64(max(items, videos)), PlaylistType: "normal"}
}

func absoluteURL(u string) string {
	if strings.HasPrefix(u, "http") {
		return u
	}
	return ""
}
