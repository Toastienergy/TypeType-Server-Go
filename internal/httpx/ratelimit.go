package httpx

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Zone is the Ktor default rate limiter: limit calls per window per key, all tokens back at once when the window
// that started with the first call has passed. Same headers, same 429 body with Retry-After.
type Zone struct {
	limit  int
	window time.Duration
	mu     sync.Mutex
	keys   map[string]*bucket
	sweep  time.Time
}

type bucket struct {
	tokens int
	since  time.Time
}

func NewZone(limit int, window time.Duration) *Zone {
	return &Zone{limit: limit, window: window, keys: map[string]*bucket{}}
}

func (z *Zone) take(key string, now time.Time) (int, time.Duration, bool) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if now.Sub(z.sweep) > z.window {
		for k, b := range z.keys {
			if now.Sub(b.since) >= z.window {
				delete(z.keys, k)
			}
		}
		z.sweep = now
	}
	b := z.keys[key]
	if b == nil || now.Sub(b.since) >= z.window {
		b = &bucket{tokens: z.limit, since: now}
		z.keys[key] = b
	}
	reset := b.since.Add(z.window).Sub(now)
	if b.tokens == 0 {
		return 0, reset, false
	}
	b.tokens--
	return b.tokens, reset, true
}

func ClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Limit wraps a handler; key picks the caller (the client IP unless the zone says otherwise).
func (z *Zone) Limit(key func(*http.Request) string, next http.HandlerFunc) http.HandlerFunc {
	if key == nil {
		key = ClientIP
	}
	return func(w http.ResponseWriter, r *http.Request) {
		left, reset, ok := z.take(key(r), time.Now())
		h := w.Header()
		secs := strconv.Itoa(int((reset + time.Second - 1) / time.Second))
		h.Set("X-RateLimit-Limit", strconv.Itoa(z.limit))
		h.Set("X-RateLimit-Remaining", strconv.Itoa(left))
		h.Set("X-RateLimit-Reset", secs)
		if !ok {
			h.Set("Retry-After", secs)
			Error(w, r, http.StatusTooManyRequests, "Too many requests", "rate_limited")
			return
		}
		next(w, r)
	}
}
