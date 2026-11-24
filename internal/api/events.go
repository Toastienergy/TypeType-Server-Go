package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

type serverNotice struct {
	Kind            string  `json:"kind"`
	Message         *string `json:"message"`
	ResumeInSeconds *int    `json:"resumeInSeconds"`
	IssuedAt        int64   `json:"issuedAt"`
}

type serverEvent struct {
	name string
	data []byte
}

// noticeHub is ServerNoticeHub: one boot id per process, the active notice, and every open event stream.
type noticeHub struct {
	bootID  string
	mu      sync.Mutex
	current *serverNotice
	subs    map[chan serverEvent]struct{}
	closed  bool
}

func newNoticeHub() *noticeHub {
	return &noticeHub{bootID: auth.UUID(), subs: map[chan serverEvent]struct{}{}}
}

func (h *noticeHub) hello() []byte {
	b, _ := json.Marshal(map[string]any{"bootId": h.bootID, "notice": h.current})
	return b
}

func (h *noticeHub) subscribe() (chan serverEvent, []byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, false
	}
	ch := make(chan serverEvent, 16)
	h.subs[ch] = struct{}{}
	return ch, h.hello(), true
}

func (h *noticeHub) unsubscribe(ch chan serverEvent) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *noticeHub) emitLocked(e serverEvent) {
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

var noticeKinds = map[string]bool{"restart": true, "update": true, "maintenance": true}

func (h *noticeHub) announce(kind string, message *string, resume *int) *serverNotice {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if message != nil {
		if t := strings.TrimSpace(*message); t == "" {
			message = nil
		} else {
			message = &t
		}
	}
	if !noticeKinds[kind] || message != nil && auth.JavaLength(*message) > 280 || resume != nil && (*resume < 0 || *resume > 86400) {
		return nil
	}
	n := &serverNotice{Kind: kind, Message: message, ResumeInSeconds: resume, IssuedAt: time.Now().UnixMilli()}
	b, _ := json.Marshal(n)
	h.mu.Lock()
	h.current = n
	h.emitLocked(serverEvent{"notice", b})
	h.mu.Unlock()
	return n
}

func (h *noticeHub) clear() {
	h.mu.Lock()
	h.current = nil
	h.emitLocked(serverEvent{"clear", []byte("{}")})
	h.mu.Unlock()
}

// shutdown tells every viewer the server restarts, then ends the streams so the HTTP server can stop.
func (h *noticeHub) shutdown() {
	resume := 20
	h.announce("restart", nil, &resume)
	h.mu.Lock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (s *Server) eventRoutes(rt *Router) {
	rt.Handle("GET", "/server/events", s.serverEvents)
	rt.Handle("POST", "/admin/server/notice", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		var req struct {
			Kind            *string `json:"kind"`
			Message         *string `json:"message"`
			ResumeInSeconds *int    `json:"resumeInSeconds"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.Kind == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		n := s.notices.announce(*req.Kind, req.Message, req.ResumeInSeconds)
		if n == nil {
			plain(w, r, http.StatusBadRequest, "SERVER_NOTICE_INVALID")
			return
		}
		httpx.JSON(w, http.StatusOK, n)
	}))
	rt.Handle("DELETE", "/admin/server/notice", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		s.notices.clear()
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (s *Server) serverEvents(w http.ResponseWriter, r *http.Request) {
	ch, hello, ok := s.notices.subscribe()
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if !ok {
		return
	}
	defer s.notices.unsubscribe(ch)
	flusher, _ := w.(http.Flusher)
	write := func(name string, data []byte) bool {
		if _, err := w.Write([]byte("event: " + name + "\ndata: " + string(data) + "\n\n")); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	if !write("hello", hello) {
		return
	}
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open || !write(e.name, e.data) {
				return
			}
		case <-beat.C:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// adminOnly is withAdminAuth: a valid token whose account is an admin.
func (s *Server) adminOnly(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.userID(w, r)
		if !ok {
			return
		}
		role, err := s.Auth.Role(r.Context(), id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if role == nil || *role != "admin" {
			plain(w, r, http.StatusForbidden, "Admin access required")
			return
		}
		next(w, r, id)
	}
}

// decodeOr reads a JSON body, answering msg as a 400 when it does not parse.
func decodeOr(w http.ResponseWriter, r *http.Request, v any, msg string) bool {
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&raw); err != nil || json.Unmarshal(raw, v) != nil {
		plain(w, r, http.StatusBadRequest, msg)
		return false
	}
	return true
}

// decodeCoded is decodeOr with an error code of its own.
func decodeCoded(w http.ResponseWriter, r *http.Request, v any, msg, code string) bool {
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&raw); err != nil || json.Unmarshal(raw, v) != nil {
		httpx.Error(w, r, http.StatusBadRequest, msg, code)
		return false
	}
	return true
}
