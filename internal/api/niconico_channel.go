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

// niconicoChannel is NiconicoUserExtractor for user pages; channel pages (ch.nicovideo.jp) are not written yet.
func (s *Server) niconicoChannel(ctx context.Context, w http.ResponseWriter, r *http.Request, raw, next string) (*youtube.Channel, bool) {
	m := nicoUserRe.FindStringSubmatch(raw)
	if m == nil {
		notWritten(w, r)
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
