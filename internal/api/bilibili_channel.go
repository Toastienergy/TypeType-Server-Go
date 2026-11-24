package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

var spaceMidRe = regexp.MustCompile(`space\.bilibili\.com/(\d+)`)

type biliSpacePage struct {
	Mid     string `json:"m"`
	Order   string `json:"o"`
	Page    int    `json:"p"`
	Mode    string `json:"x,omitempty"`
	LastAid int64  `json:"a,omitempty"`
}

func biliSpaceVideo(o map[string]any, name, face string) youtube.VideoItem {
	s := func(k string) string { v, _ := o[k].(string); return v }
	n := func(k string) int64 { f, _ := o[k].(float64); return int64(f) }
	bv := s("bvid")
	pic := s("pic")
	if pic == "" {
		pic = s("cover")
	}
	it := youtube.VideoItem{ID: bv, Title: s("title"), URL: "https://www.bilibili.com/video/" + bv + "?p=1", ThumbnailURL: https(pic),
		UploaderName: name, UploaderAvatarURL: face, Uploaded: -1, StreamType: "video_stream"}
	if a := s("author"); a != "" {
		it.UploaderName = a
	}
	it.Duration = n("duration")
	if it.Duration == 0 {
		it.Duration = biliDuration(s("length"))
	}
	if it.ViewCount = n("play"); it.ViewCount <= 0 {
		if st, ok := o["stat"].(map[string]any); ok {
			f, _ := st["view"].(float64)
			it.ViewCount = int64(f)
		}
	}
	if mid := n("mid"); mid > 0 {
		it.UploaderURL = spaceURL(mid)
	}
	created := n("created")
	if created == 0 {
		created = n("pubdate")
	}
	if created == 0 {
		created = n("ctime")
	}
	if created > 0 {
		it.Uploaded = created * 1000
		it.UploadDate = time.Unix(created, 0).UTC().Format("2006-01-02 15:04:05")
		p := it.Uploaded
		it.PublishedAt = &p
	}
	return it
}

func (s *Server) bilibiliChannel(ctx context.Context, w http.ResponseWriter, r *http.Request, raw, next, sort string) (*youtube.Channel, bool) {
	pg := biliSpacePage{Order: "pubdate", Page: 1}
	if sort == "popular" {
		pg.Order = "click"
	}
	if m := spaceMidRe.FindStringSubmatch(raw); m != nil {
		pg.Mid = m[1]
	}
	if next != "" {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(next, "bili1."))
		if !strings.HasPrefix(next, "bili1.") || err != nil || json.Unmarshal(b, &pg) != nil || pg.Page < 2 {
			plain(w, r, http.StatusBadRequest, "Invalid channel URL")
			return nil, false
		}
	}
	if pg.Mid == "" {
		plain(w, r, http.StatusBadRequest, "Invalid channel URL")
		return nil, false
	}
	cardc := make(chan map[string]any, 1)
	go func() {
		c, _ := s.bili.Card(ctx, pg.Mid)
		cardc <- c
	}()
	sp, err := s.bili.SpaceVideos(ctx, pg.Mid, pg.Order, pg.Page, pg.Mode, pg.LastAid)
	vids := sp.Items
	card := <-cardc
	if err != nil && card == nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not load this channel", "error")
		return nil, false
	}
	cd, _ := card["card"].(map[string]any)
	space, _ := card["space"].(map[string]any)
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	fans, _ := cd["fans"].(float64)
	ch := &youtube.Channel{Name: str(cd, "name"), Description: str(cd, "sign"), AvatarURL: https(str(cd, "face")), BannerURL: https(str(space, "l_img")),
		SubscriberCount: int64(fans), Videos: []youtube.VideoItem{}}
	for _, k := range []string{"official_verify", "Official"} {
		if v, ok := cd[k].(map[string]any); ok {
			if t, ok := v["type"].(float64); ok && (t == 0 || t == 1) {
				label := "personal"
				if t == 1 {
					label = "organization"
				}
				ch.IsVerified, ch.Verification = true, &label
				break
			}
		}
	}
	for _, o := range vids {
		ch.Videos = append(ch.Videos, biliSpaceVideo(o, ch.Name, ch.AvatarURL))
	}
	if len(vids) > 0 {
		b, _ := json.Marshal(biliSpacePage{Mid: pg.Mid, Order: pg.Order, Page: pg.Page + 1, Mode: sp.Mode, LastAid: sp.LastAid})
		n := "bili1." + base64.RawURLEncoding.EncodeToString(b)
		ch.NextPage = &n
	}
	return ch, true
}
