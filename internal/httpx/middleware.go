package httpx

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{8,128}$`)

const (
	RequestIDHeader     = "X-Request-ID"
	PlaybackTraceHeader = "X-Playback-Trace-ID"
)

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Observe gives every call a request id (kept from the client when well formed), echoes it and the playback trace
// id, logs one line per call in the Kotlin format and turns a panic into the same 500 body.
func Observe(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get(RequestIDHeader)
		if !requestIDRe.MatchString(id) {
			id = uuid()
		}
		w.Header().Set(RequestIDHeader, id)
		if t := r.Header.Get(PlaybackTraceHeader); requestIDRe.MatchString(t) {
			w.Header().Set(PlaybackTraceHeader, t)
		}
		sw := &statusWriter{ResponseWriter: w}
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				log.Error("unhandled panic", "requestId", id, "path", r.URL.Path, "panic", fmt.Sprint(p))
				if !sw.wrote {
					Error(sw, r, http.StatusInternalServerError, "Internal server error", "internal_error")
				}
			}
			status := sw.status
			if !sw.wrote {
				status = http.StatusOK
			}
			log.Info("call", "requestId", id, "method", r.Method, "path", MetricPath(r.URL.Path), "status", status,
				"durationMs", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(sw, r)
	})
}

func MetricPath(p string) string {
	switch {
	case strings.HasPrefix(p, "/api/sabr/playback/"):
		return "/api/sabr/playback/{sessionId}"
	case strings.HasPrefix(p, "/sabr/playback/"):
		return "/sabr/playback/{sessionId}"
	case strings.HasPrefix(p, "/downloader/jobs/"):
		for _, s := range []string{"/events", "/artifact", "/cancel"} {
			if strings.HasSuffix(p, s) {
				return "/downloader/jobs/{id}" + s
			}
		}
		return "/downloader/jobs/{id}"
	case p == "/progress/batch":
		return p
	case strings.HasPrefix(p, "/progress/"):
		return "/progress/{videoUrl}"
	case strings.HasPrefix(p, "/favorites/"):
		return "/favorites/{videoUrl}"
	case strings.HasPrefix(p, "/watch-later/"):
		return "/watch-later/{videoUrl}"
	case strings.HasPrefix(p, "/subscriptions/"):
		return "/subscriptions/{channelUrl}"
	case p == "":
		return "/"
	}
	return p
}

func uuid() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CORS follows the Ktor plugin as configured: listed origins (or *), credentials, Content-Type and Authorization
// on top of the simple headers, PUT and DELETE on top of the simple methods. A foreign origin gets 403.
func CORS(allowed map[string]bool, any bool, next http.Handler) http.Handler {
	methods := map[string]bool{"GET": true, "POST": true, "HEAD": true, "PUT": true, "DELETE": true}
	headers := map[string]bool{"accept": true, "accept-language": true, "content-language": true, "content-type": true, "authorization": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || sameOrigin(r, origin) {
			next.ServeHTTP(w, r)
			return
		}
		if !any && !allowed[origin] {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !methods[r.Header.Get("Access-Control-Request-Method")] {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			for _, name := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				if name = strings.ToLower(strings.TrimSpace(name)); name != "" && !headers[name] {
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}
			h.Set("Access-Control-Allow-Methods", "DELETE, PUT")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request, origin string) bool {
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return o == r.Host
}
