package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	liveChatSessions  = 12
	liveChatPoll      = time.Second
	liveChatHeartbeat = 15 * time.Second
	liveChatOpenWait  = 25 * time.Second
)

var liveChatSlots = make(chan struct{}, liveChatSessions)

type liveChatMessage struct {
	ID              string  `json:"id"`
	Text            string  `json:"text"`
	ReceivedAtMs    int64   `json:"receivedAtMs"`
	AuthorName      *string `json:"authorName"`
	AuthorAvatarURL *string `json:"authorAvatarUrl"`
	Moderator       bool    `json:"moderator"`
}

// liveChat is YoutubeLiveChatRoutes over PipePipeYoutubeLiveChatService: an event stream of the chat of a live
// YouTube video, polled every second like PipePipe's polling task, with a keep-alive comment every 15 s.
func (s *Server) liveChat(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.publicAccess(w, r); !ok {
		return
	}
	raw := r.URL.Query().Get("url")
	if raw == "" {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	id := YouTubeID(raw)
	if id == "" {
		msg := "Unsupported video URL"
		if bilibiliHost(raw) || isNiconico(raw) {
			msg = "Live chat is only available for YouTube"
		}
		plain(w, r, http.StatusUnprocessableEntity, msg)
		return
	}
	select {
	case liveChatSlots <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "5")
		plain(w, r, http.StatusTooManyRequests, "Live chat capacity is temporarily full")
		return
	}
	defer func() { <-liveChatSlots }()
	ctx, cancel := context.WithTimeout(r.Context(), liveChatOpenWait)
	info, err := s.Sabr.Token().Info(ctx, id)
	if err != nil {
		cancel()
		plain(w, r, http.StatusBadGateway, "YouTube session is unavailable")
		return
	}
	if !info.Live && !info.Meta.IsLive {
		cancel()
		plain(w, r, http.StatusUnprocessableEntity, "Live chat requires a live YouTube video")
		return
	}
	continuation, err := s.YT.LiveChatStart(ctx, id)
	cancel()
	switch {
	case err != nil && ctx.Err() != nil:
		plain(w, r, http.StatusBadGateway, "YouTube did not respond in time")
		return
	case err != nil:
		plain(w, r, http.StatusBadGateway, "Could not start YouTube live chat")
		return
	case continuation == "":
		plain(w, r, http.StatusUnprocessableEntity, "Live chat is unavailable for this video")
		return
	}
	flusher, _ := w.(http.Flusher)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(s string) bool {
		if _, err := fmt.Fprint(w, s); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	if !send(": connected\n\n") {
		return
	}
	start, seq := time.Now(), 0
	seen := map[string]bool{}
	lastBeat := start
	tick := time.NewTicker(liveChatPoll)
	defer tick.Stop()
	for {
		now := time.Now()
		pctx, pcancel := context.WithTimeout(r.Context(), 10*time.Second)
		msgs, next, err := s.YT.LiveChatPoll(pctx, continuation, max(now.Sub(start).Milliseconds(), 1))
		pcancel()
		if err == nil && next != "" {
			continuation = next
		}
		for _, m := range msgs {
			if m.Text == "" || seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			seq++
			out := liveChatMessage{ID: fmt.Sprintf("chat-%d", seq), Text: m.Text, ReceivedAtMs: now.UnixMilli(), Moderator: m.Moderator}
			if m.AuthorName != "" {
				out.AuthorName = &m.AuthorName
			}
			if m.AuthorAvatarURL != "" {
				out.AuthorAvatarURL = &m.AuthorAvatarURL
			}
			b, _ := json.Marshal(out)
			if !send("event: message\ndata: " + string(b) + "\n\n") {
				return
			}
		}
		if now.Sub(lastBeat) >= liveChatHeartbeat {
			if !send(": keep-alive\n\n") {
				return
			}
			lastBeat = now
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Server) liveChatRoutes(rt *Router) {
	rt.Handle("GET", "/live-chat", s.liveChat)
}
