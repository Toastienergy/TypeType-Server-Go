package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

var postsTabSuffix = regexp.MustCompile(`(?i)/(posts|community)$`)

// postsChannelURL is toPostsChannelUrl: the channel URL without query, fragment, trailing slash or posts tab.
func postsChannelURL(raw string) string {
	v := strings.TrimSpace(raw)
	v, _, _ = strings.Cut(v, "?")
	v, _, _ = strings.Cut(v, "#")
	v = strings.TrimRight(v, "/")
	if loc := postsTabSuffix.FindStringIndex(v); loc != nil {
		return v[:loc[0]]
	}
	return v
}

// channelPosts is /channel/posts: a YouTube channel's posts tab, kept 15 minutes when it has posts, then the
// allow list, the blocked channels and Focus mode, as ChannelPostsRoutes checks them.
func (s *Server) channelPosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !q.Has("url") {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	raw, page := q.Get("url"), q.Get("nextpage")
	if strings.TrimSpace(page) == "" {
		page = ""
	}
	user, ok := s.publicAccess(w, r)
	if !ok {
		return
	}
	target := postsChannelURL(raw)
	u, err := url.Parse(target)
	host := ""
	if err == nil {
		host = strings.ToLower(u.Hostname())
	}
	switch {
	case host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be":
	case bilibiliHost(target) || strings.Contains(host, "nicovideo.jp"):
		plain(w, r, http.StatusBadRequest, "Posts are only supported for YouTube channels")
		return
	default:
		plain(w, r, http.StatusBadRequest, "Unsupported channel URL")
		return
	}
	sum := sha256.Sum256([]byte(raw + "\x00" + page))
	key := "go:channel-posts:" + hex.EncodeToString(sum[:16])
	var res youtube.ChannelPosts
	cached := false
	if v, hit, _ := s.Cache.Get(r.Context(), key); hit && json.Unmarshal([]byte(v), &res) == nil {
		cached = true
	}
	if !cached {
		res, err = s.YT.ChannelPosts(r.Context(), target, page)
		switch {
		case errors.Is(err, youtube.ErrBadPage):
			plain(w, r, http.StatusBadRequest, "Invalid nextpage cursor")
			return
		case errors.Is(err, youtube.ErrNotChannel):
			plain(w, r, http.StatusBadRequest, "Unsupported channel URL")
			return
		case err != nil:
			s.Log.Warn("channel_posts", "url", raw, "err", err)
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Channel posts extraction failed", "error")
			return
		}
		if len(res.Posts) > 0 {
			if b, err := json.Marshal(res); err == nil {
				s.Cache.Set(r.Context(), key, string(b), 15*time.Minute)
			}
		}
	}
	name := res.ChannelName
	if user != "" {
		if !s.accessFor(r.Context(), user).allowsChannel(raw, strings.TrimSpace(name)) {
			plain(w, r, http.StatusForbidden, "Channel is not allowed")
			return
		}
		blocked, err := s.blockedFilter(r, user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if blocked(youtube.VideoItem{UploaderURL: raw, UploaderName: name}) {
			httpx.Error(w, r, http.StatusForbidden, "Channel is blocked", "content_blocked")
			return
		}
		if b := s.focusFor(r.Context(), user).channelBlock(raw, name); b != nil {
			b["requestId"] = httpx.RequestID(r.Context())
			httpx.JSON(w, http.StatusForbidden, b)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, res)
}
