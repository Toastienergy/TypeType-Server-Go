package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/niconico"
	"typetype.dev/server/internal/youtube"
)

func niconicoRelatedItem(o map[string]any) (youtube.VideoItem, bool) {
	if ct, _ := o["contentType"].(string); ct == "mylist" {
		return youtube.VideoItem{}, false
	}
	c, _ := o["content"].(map[string]any)
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	num := func(m map[string]any, k string) int64 { f, _ := m[k].(float64); return int64(f) }
	id := str(c, "id")
	if id == "" {
		return youtube.VideoItem{}, false
	}
	thumb, _ := c["thumbnail"].(map[string]any)
	owner, _ := c["owner"].(map[string]any)
	count, _ := c["count"].(map[string]any)
	it := youtube.VideoItem{ID: id, Title: str(c, "title"), URL: "https://www.nicovideo.jp/watch/" + id, ThumbnailURL: str(thumb, "listingUrl"),
		UploaderName: str(owner, "name"), UploaderAvatarURL: str(owner, "iconUrl"), Duration: num(c, "duration"), ViewCount: num(count, "view"),
		Uploaded: -1, StreamType: "video_stream"}
	if it.ThumbnailURL == "" {
		it.ThumbnailURL = str(thumb, "url")
	}
	switch str(owner, "ownerType") {
	case "user":
		it.UploaderURL = "https://www.nicovideo.jp/user/" + str(owner, "id")
	case "channel":
		it.UploaderURL = "https://ch.nicovideo.jp/" + str(owner, "id")
	}
	if t, err := time.Parse(time.RFC3339, str(c, "registeredAt")); err == nil {
		it.Uploaded = t.UnixMilli()
		it.UploadDate = str(c, "registeredAt")
		p := it.Uploaded
		it.PublishedAt = &p
	}
	return it, true
}

func (s *Server) niconicoStream(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	id := niconico.IDRe.FindString(raw)
	if !strings.Contains(strings.ToLower(raw), "nicovideo.jp") && !strings.Contains(strings.ToLower(raw), "nico.ms") || id == "" {
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
		rel, _ := s.nico.Related(ctx, id)
		relc <- rel
	}()
	v, err := s.nico.Watch(ctx, id)
	if err != nil {
		if errors.Is(err, niconico.ErrUnavailable) {
			httpx.Error(w, r, http.StatusUnprocessableEntity, err.Error(), "content_unavailable")
		} else {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not extract this video", "extraction_failed")
		}
		return
	}
	tags := v.Tags
	if tags == nil {
		tags = []string{}
	}
	resp := streamResponse{ID: v.ID, Title: v.Title, Description: v.Description, UploaderName: v.OwnerName, UploaderURL: v.OwnerURL,
		UploaderAvatarURL: v.OwnerIcon, ThumbnailURL: v.Thumbnail, Duration: v.Duration, ViewCount: v.Views, LikeCount: v.Likes, DislikeCount: -1,
		Uploaded: -1, UploaderSubscriberCount: -1, Visibility: "public", Tags: tags, StreamType: "video_stream", StreamSegments: []youtube.Chapter{},
		VideoStreams: []videoStream{}, AudioStreams: []audioStream{}, VideoOnlyStreams: []videoStream{}, Subtitles: []youtube.Subtitle{},
		PreviewFrames: []youtube.Frames{}, SponsorBlockSegments: []sponsorSegment{}, RelatedStreams: []youtube.VideoItem{}, Collections: []any{},
		Parts: []any{}, EndScreenElements: []any{}, StartPosition: startParam(raw)}
	if !v.Registered.IsZero() {
		resp.Uploaded = v.Registered.UnixMilli()
		resp.UploadDate = v.Registered.Format(time.RFC3339)
		p := resp.Uploaded
		resp.PublishedAt = &p
	}
	if hidden(youtube.VideoItem{URL: raw, UploaderURL: resp.UploaderURL, UploaderName: resp.UploaderName}) {
		httpx.Error(w, r, http.StatusForbidden, "Channel is blocked", "content_blocked")
		return
	}
	if b := focus.playbackBlock(resp.UploaderURL, resp.PublishedAt); b != nil {
		b["channelUrl"], b["channelName"], b["channelAvatarUrl"], b["videoTitle"] = resp.UploaderURL, resp.UploaderName, resp.UploaderAvatarURL, resp.Title
		b["requestId"] = httpx.RequestID(r.Context())
		httpx.JSON(w, http.StatusForbidden, b)
		return
	}
	bid := &v.Bid
	vids, auds := make([]*videoStream, len(v.Videos)), make([]*audioStream, len(v.Audios))
	var wg sync.WaitGroup
	for i, x := range v.Videos {
		wg.Go(func() {
			if p := s.mediaPath(ctx, x.URL, nil, bid); p != "" {
				vids[i] = &videoStream{URL: p, MimeType: "video/mp4", Format: "MPEG_4", Resolution: x.Resolution, IsVideoOnly: true, Itag: -1,
					DeliveryMethod: "hls"}
			}
		})
	}
	for i, x := range v.Audios {
		wg.Go(func() {
			if p := s.mediaPath(ctx, x.URL, nil, bid); p != "" {
				q := strconv.FormatInt(x.Kbps, 10)
				br := x.Kbps
				auds[i] = &audioStream{URL: p, MimeType: "audio/mp4", Format: "M4A", Bitrate: &br, Quality: &q, Itag: -1, DeliveryMethod: "progressive"}
			}
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
	for _, o := range <-relc {
		if it, ok := niconicoRelatedItem(o); ok && !hidden(it) && (focus == nil || focus.keepsFeedVideo(it)) {
			resp.RelatedStreams = append(resp.RelatedStreams, it)
		}
	}
	if len(resp.VideoOnlyStreams) == 0 && len(resp.AudioStreams) == 0 {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "No playable streams", "no_playable_streams")
		return
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// rewriteProviderHLS points every playlist, key and segment of a provider playlist at its own media handle, keeping
// the bid cookie, as rewriteProviderHlsManifest does.
func (s *Server) rewriteProviderHLS(ctx context.Context, body, base string, bid *string) string {
	b, err := url.Parse(base)
	if err != nil {
		return body
	}
	resolve := func(ref string) string {
		u, err := b.Parse(ref)
		if err != nil {
			return ref
		}
		return u.String()
	}
	refs := map[string]string{}
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
			for _, m := range uriAttr.FindAllStringSubmatch(t, -1) {
				refs[resolve(m[1])] = ""
			}
		default:
			refs[resolve(t)] = ""
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 32)
	for ref := range refs {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			if p := s.mediaPath(ctx, ref, nil, bid); p != "" {
				mu.Lock()
				refs[ref] = "../media/" + strings.TrimPrefix(p, "/media/")
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	mapped := func(raw string) string {
		r := resolve(raw)
		if m := refs[r]; m != "" {
			return m
		}
		return r
	}
	for i, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
			lines[i] = uriAttr.ReplaceAllStringFunc(t, func(m string) string { return `URI="` + mapped(uriAttr.FindStringSubmatch(m)[1]) + `"` })
		default:
			lines[i] = mapped(t)
		}
	}
	return strings.Join(lines, "\n")
}

func isHLSResponse(ct, target string) bool {
	c := strings.ToLower(ct)
	u, _ := url.Parse(target)
	return strings.Contains(c, "mpegurl") || u != nil && strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

func readHLS(res *http.Response) (string, error) {
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	return string(b), err
}
