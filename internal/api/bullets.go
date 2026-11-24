package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"typetype.dev/server/internal/bilibili"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/niconico"
)

type bulletItem struct {
	Text             string  `json:"text"`
	ArgbColor        int32   `json:"argbColor"`
	Position         string  `json:"position"`
	RelativeFontSize float64 `json:"relativeFontSize"`
	DurationMs       int64   `json:"durationMs"`
	IsLive           bool    `json:"isLive"`
}

// bulletRoutes is PipePipeBulletCommentService for recorded videos: Bilibili danmaku and NicoNico comments; YouTube
// has none, and the live chats of either service are not served here.
func (s *Server) bulletRoutes(rt *Router) {
	rt.Handle("GET", "/bullet-comments", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		raw := r.URL.Query().Get("url")
		if !r.URL.Query().Has("url") {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		if r.URL.Query().Get("nextpage") != "" {
			plain(w, r, http.StatusBadRequest, "Invalid nextpage cursor")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		items := []bulletItem{}
		lower := strings.ToLower(raw)
		var err error
		switch {
		case strings.Contains(lower, "live.bilibili.com"), strings.Contains(lower, "live.nicovideo.jp"):
		case bilibiliHost(raw):
			var cid int64
			if cid, err = s.bili.CID(ctx, raw); err == nil {
				var bs []bilibili.Bullet
				if bs, err = s.bili.Danmaku(ctx, cid); err == nil {
					for _, b := range bs {
						items = append(items, bulletItem{b.Text, b.ARGB, b.Position, b.Size, b.AtMs, false})
					}
				}
			}
		case strings.Contains(lower, "nicovideo.jp") || strings.Contains(lower, "nico.ms"):
			id := niconico.IDRe.FindString(raw)
			var bs []niconico.Bullet
			if bs, err = s.nico.Comments(ctx, id); err == nil {
				for _, b := range bs {
					items = append(items, bulletItem{b.Text, b.ARGB, b.Position, b.Size, b.AtMs, false})
				}
			}
		}
		if err != nil {
			if errors.Is(err, bilibili.ErrRiskControl) {
				w.Header().Set("Retry-After", "30")
				httpx.Error(w, r, http.StatusServiceUnavailable, "Bilibili risk control blocked this server", "provider_access_blocked")
				return
			}
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Bullet comment extraction failed", "error")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"comments": items, "nextpage": nil})
	})
}
