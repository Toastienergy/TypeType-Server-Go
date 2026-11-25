package api

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

var nicoUserRe = regexp.MustCompile(`nicovideo\.jp/user/(\d+)`)

// niconicoChannel is NiconicoUserExtractor: a user's uploads, or a channel's (ch.nicovideo.jp) video feed.
func (s *Server) niconicoChannel(ctx context.Context, w http.ResponseWriter, r *http.Request, raw, next string) (*youtube.Channel, bool) {
	if strings.Contains(strings.ToLower(raw), "ch.nicovideo.jp/") {
		return s.niconicoChannelFeed(ctx, w, r, raw, next)
	}
	m := nicoUserRe.FindStringSubmatch(raw)
	if m == nil {
		plain(w, r, http.StatusBadRequest, "Invalid channel URL")
		return nil, false
	}
	page := 1
	if next != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(next, "nico1.u"))
		if !strings.HasPrefix(next, "nico1.u") || err != nil || n < 2 {
			plain(w, r, http.StatusBadRequest, "Invalid channel URL")
			return nil, false
		}
		page = n
	}
	prof, items, total, err := s.nico.User(ctx, m[1], page)
	if err != nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not load this channel", "error")
		return nil, false
	}
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	icons, _ := prof["icons"].(map[string]any)
	followers, _ := prof["followerCount"].(float64)
	ch := &youtube.Channel{Name: str(prof, "nickname"), Description: str(prof, "description"), AvatarURL: str(icons, "large"),
		SubscriberCount: int64(followers), Videos: []youtube.VideoItem{}}
	for _, o := range items {
		ch.Videos = append(ch.Videos, niconicoSearchItem(o))
	}
	if len(items) > 0 && int64(page)*100 < total {
		n := "nico1.u" + strconv.Itoa(page+1)
		ch.NextPage = &n
	}
	return ch, true
}

var nicoJapaneseDate = regexp.MustCompile(`\d{4}年\d{2}月\d{2}日 \d{2}[：:]\d{2}[：:]\d{2}`)

// niconicoChannelFeed is the channel half of NiconicoUserExtractor: twenty videos a page from the channel's RSS feed,
// no follower count.
func (s *Server) niconicoChannelFeed(ctx context.Context, w http.ResponseWriter, r *http.Request, raw, next string) (*youtube.Channel, bool) {
	page := 1
	if next != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(next, "nico1.c"))
		if !strings.HasPrefix(next, "nico1.c") || err != nil || n < 2 {
			plain(w, r, http.StatusBadRequest, "Invalid channel URL")
			return nil, false
		}
		page = n
	}
	channelURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	info, videos, err := s.nico.Channel(ctx, channelURL, page)
	if err != nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not load this channel", "error")
		return nil, false
	}
	ch := &youtube.Channel{Name: info.Name, Description: info.Description, AvatarURL: info.AvatarURL, SubscriberCount: -1, Videos: []youtube.VideoItem{}}
	for _, v := range videos {
		it := youtube.VideoItem{ID: v.URL, Title: v.Title, URL: v.URL, ThumbnailURL: absoluteURL(v.ThumbnailURL), UploaderName: info.Name,
			UploaderURL: channelURL, UploaderAvatarURL: info.AvatarURL, Duration: v.Duration, ViewCount: v.ViewCount, Uploaded: v.Uploaded,
			StreamType: "video_stream", RequiresMembership: v.Premium, UploadDate: v.UploadDate}
		if d := nicoJapaneseDate.FindString(v.UploadDate); d != "" {
			it.UploadDate = d
		}
		if v.Uploaded > 0 {
			p := v.Uploaded
			it.PublishedAt = &p
		}
		ch.Videos = append(ch.Videos, it)
	}
	if len(videos) > 0 {
		n := "nico1.c" + strconv.Itoa(page+1)
		ch.NextPage = &n
	}
	return ch, true
}
