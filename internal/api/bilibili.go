package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/bilibili"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

var bilibiliQualities = map[int]string{127: "8K 超高清", 126: "杜比视界", 125: "HDR 真彩色", 120: "4K 超清", 116: "1080P60 高帧率",
	112: "1080P+ 高码率", 80: "1080P 高清", 74: "720P60 高帧率", 64: "720P 高清", 32: "480P 清晰", 16: "360P 流畅", 6: "240P 极速"}

var bilibiliBitrates = map[int]string{30216: "64K", 30232: "132K", 30280: "192K", 30250: "杜比全景声", 30255: "杜比全景声", 30251: "Hi-Res无损"}

var beijing = time.FixedZone("CST", 8*3600)

func bilibiliHost(raw string) bool {
	h := strings.ToLower(raw)
	return strings.Contains(h, "bilibili.com/") || strings.Contains(h, "b23.tv/")
}

func spaceURL(mid int64) string { return "https://space.bilibili.com/" + strconv.FormatInt(mid, 10) }

func https(u string) string { return strings.Replace(u, "http:", "https:", 1) }

func bilibiliItem(o map[string]any) youtube.VideoItem {
	bv, _ := o["bvid"].(string)
	owner, _ := o["owner"].(map[string]any)
	stat, _ := o["stat"].(map[string]any)
	num := func(m map[string]any, k string) int64 {
		f, _ := m[k].(float64)
		return int64(f)
	}
	s := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	it := youtube.VideoItem{ID: bv, Title: s(o, "title"), URL: "https://www.bilibili.com/video/" + bv + "?p=1", ThumbnailURL: https(s(o, "pic")),
		UploaderName: s(owner, "name"), UploaderAvatarURL: strings.Replace(s(owner, "face"), "http", "https", 1), Duration: num(o, "duration"),
		ViewCount: num(stat, "view"), Uploaded: -1, StreamType: "video_stream"}
	if mid := num(owner, "mid"); mid > 0 {
		it.UploaderURL = spaceURL(mid)
	}
	if pd := num(o, "pubdate"); pd > 0 {
		it.Uploaded = pd * 1000
		it.UploadDate = time.Unix(pd, 0).In(beijing).Format("2006-01-02 15:04:05")
		p := it.Uploaded
		it.PublishedAt = &p
	}
	return it
}

func (s *Server) bilibiliStream(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	if !bilibiliHost(raw) {
		httpx.Error(w, r, http.StatusBadRequest, "URL does not match stream endpoint", "provider_mismatch")
		return
	}
	user, ok := s.publicAccess(w, r)
	if !ok {
		return
	}
	hidden := func(youtube.VideoItem) bool { return false }
	var focus *focusProfile
	if user != "" && !strings.HasPrefix(user, "guest:") {
		if h, err := s.blockedFilter(r, user); err == nil {
			hidden = h
		}
		focus = s.focusFor(r.Context(), user)
	}
	if hidden(youtube.VideoItem{URL: raw}) {
		httpx.Error(w, r, http.StatusForbidden, "Video is blocked", "content_blocked")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	relc := make(chan []map[string]any, 1)
	go func() {
		rel, _ := s.bili.Related(ctx, bilibili.BVID(raw))
		relc <- rel
	}()
	vctx, account := ctx, false
	if user != "" && !strings.HasPrefix(user, "guest:") {
		if cookies := s.biliAccountCookies(ctx, user); cookies != "" {
			if !biliAccountRate.allow(user) {
				httpx.Error(w, r, http.StatusUnprocessableEntity, "BiliBili is temporarily limiting requests. Try again later.", "bilibili_rate_limited")
				return
			}
			vctx, account = bilibili.WithAccount(ctx, cookies), true
		}
	}
	v, err := s.bili.Video(vctx, raw)
	if account {
		if err == nil {
			s.DB.Exec(context.WithoutCancel(ctx), "UPDATE bilibili_sessions SET last_used_at = $2 WHERE user_id = $1", user, time.Now().UnixMilli())
		} else if msg := err.Error(); errors.Is(err, bilibili.ErrRiskControl) || strings.Contains(msg, "412") || strings.Contains(msg, "-352") ||
			strings.Contains(strings.ToLower(msg), "risk") {
			s.biliNeedsReconnect(context.WithoutCancel(ctx), user)
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, bilibili.ErrRiskControl):
			w.Header().Set("Retry-After", "30")
			httpx.Error(w, r, http.StatusServiceUnavailable, "Bilibili risk control blocked this server", "provider_access_blocked")
		case errors.Is(err, bilibili.ErrUnavailable):
			httpx.Error(w, r, http.StatusUnprocessableEntity, err.Error(), "content_unavailable")
		default:
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not extract this video", "extraction_failed")
		}
		return
	}
	resp := streamResponse{ID: v.BVID, Title: v.Title, Description: v.Description, UploaderName: v.OwnerName, UploaderURL: spaceURL(v.OwnerMid),
		UploaderAvatarURL: https(v.OwnerFace), ThumbnailURL: v.Thumbnail, Duration: v.Duration, ViewCount: v.Views, LikeCount: v.Likes,
		DislikeCount: -1, Uploaded: -1, UploaderSubscriberCount: -1, Visibility: "public", Tags: []string{}, StreamType: "video_stream",
		StreamSegments: []youtube.Chapter{}, VideoStreams: []videoStream{}, AudioStreams: []audioStream{}, VideoOnlyStreams: []videoStream{},
		Subtitles: []youtube.Subtitle{}, PreviewFrames: []youtube.Frames{}, SponsorBlockSegments: []sponsorSegment{},
		RelatedStreams: []youtube.VideoItem{}, Collections: []any{}, Parts: []any{}, EndScreenElements: []any{}, StartPosition: startParam(raw)}
	resp.Collections = v.Collections
	for _, pg := range v.Pages {
		resp.Parts = append(resp.Parts, map[string]any{"page": pg.Number, "title": pg.Title,
			"url": "https://www.bilibili.com/video/" + v.BVID + "?p=" + strconv.Itoa(pg.Number), "thumbnailUrl": v.Thumbnail, "duration": pg.Duration})
	}
	if v.PubDate > 0 {
		resp.Uploaded = v.PubDate * 1000
		resp.UploadDate = time.Unix(v.PubDate, 0).In(beijing).Format("2006-01-02 15:04:05")
		p := resp.Uploaded
		resp.PublishedAt = &p
	}
	if hidden(youtube.VideoItem{URL: raw, UploaderURL: resp.UploaderURL, UploaderName: resp.UploaderName}) {
		httpx.Error(w, r, http.StatusForbidden, "Channel is blocked", "content_blocked")
		return
	}
	access := s.accessFor(r.Context(), user)
	if !access.allowsUploader(resp.UploaderURL, resp.UploaderName) {
		plain(w, r, http.StatusForbidden, "Channel is not allowed")
		return
	}
	if b := focus.playbackBlock(resp.UploaderURL, resp.PublishedAt); b != nil {
		b["channelUrl"], b["channelName"], b["channelAvatarUrl"], b["videoTitle"] = resp.UploaderURL, resp.UploaderName, resp.UploaderAvatarURL, resp.Title
		b["requestId"] = httpx.RequestID(r.Context())
		httpx.JSON(w, http.StatusForbidden, b)
		return
	}
	vids, auds := make([]*videoStream, len(v.VideoOnly)), make([]*audioStream, len(v.Audio))
	var wg sync.WaitGroup
	for i, m := range v.VideoOnly {
		wg.Go(func() {
			path := s.mediaPath(ctx, m.URL, m.Alternates, nil)
			if path == "" {
				return
			}
			br, codec := m.Bandwidth, m.Codec
			vs := videoStream{URL: path, MimeType: "video/mp4", Format: "MPEG_4", Resolution: bilibiliQualities[m.QualityID], Bitrate: &br, Codec: &codec,
				IsVideoOnly: true, Itag: -1, Width: m.Width, Height: m.Height, Fps: m.Fps, InitStart: m.InitStart, InitEnd: m.InitEnd,
				IndexStart: m.IndexStart, IndexEnd: m.IndexEnd, DeliveryMethod: "progressive"}
			if vs.Resolution == "" {
				vs.Resolution = "Unknown resolution"
			}
			vids[i] = &vs
		})
	}
	for i, m := range v.Audio {
		wg.Go(func() {
			path := s.mediaPath(ctx, m.URL, m.Alternates, nil)
			if path == "" {
				return
			}
			br := m.Bandwidth / 1000
			codec, _, _ := strings.Cut(m.Codec, ".")
			q := bilibiliBitrates[m.QualityID]
			if q == "" {
				q = "Unknown bitrate"
			}
			as := audioStream{URL: path, MimeType: "audio/mp4", Format: "M4A", Bitrate: &br, Codec: &codec, Quality: &q, Itag: -1,
				InitStart: m.InitStart, InitEnd: m.InitEnd, IndexStart: m.IndexStart, IndexEnd: m.IndexEnd, DeliveryMethod: "progressive"}
			auds[i] = &as
		})
	}
	wg.Wait()
	for _, x := range vids {
		if x != nil {
			resp.VideoOnlyStreams = append(resp.VideoOnlyStreams, *x)
		}
	}
	for _, x := range auds {
		if x != nil {
			resp.AudioStreams = append(resp.AudioStreams, *x)
		}
	}
	if !internalLookup(r.Context()) && (len(resp.VideoOnlyStreams) > 0 || len(resp.AudioStreams) > 0) {
		s.warmStreams("bilibili", append(append([]videoStream{}, resp.VideoOnlyStreams...), resp.VideoStreams...), resp.AudioStreams)
	}
	for _, o := range <-relc {
		it := bilibiliItem(o)
		if !hidden(it) && (focus == nil || focus.keepsFeedVideo(it)) && access.allowsUploader(it.UploaderURL, it.UploaderName) {
			resp.RelatedStreams = append(resp.RelatedStreams, it)
		}
	}
	if len(resp.VideoOnlyStreams) == 0 && len(resp.AudioStreams) == 0 {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "No playable streams", "no_playable_streams")
		return
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// bilibiliTrendingItem is BilibiliTrendingService's mapping, which keeps the card's own uri and the "video" type.
func bilibiliTrendingItem(o map[string]any) youtube.VideoItem {
	it := bilibiliItem(o)
	if uri, _ := o["uri"].(string); uri != "" {
		it.URL = uri + "?p=1"
	}
	owner, _ := o["owner"].(map[string]any)
	it.ID, it.StreamType = it.URL, "video"
	it.UploaderAvatarURL, _ = owner["face"].(string)
	if it.Uploaded > 0 {
		it.UploadDate = time.Unix(it.Uploaded/1000, 0).UTC().Format("2006-01-02 15:04:05")
	}
	return it
}

func (s *Server) bilibiliTrending(ctx context.Context) ([]youtube.VideoItem, error) {
	items, err := s.bili.Trending(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]youtube.VideoItem, 0, len(items))
	for _, o := range items {
		out = append(out, bilibiliTrendingItem(o))
	}
	return out, nil
}
