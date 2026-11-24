package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/httpx"
)

// internalAuthorized answers whether the request carries INTERNAL_OBSERVABILITY_TOKEN; without one configured the
// internal routes do not exist (404), like the Kotlin server.
func internalAuthorized(r *http.Request) bool {
	want := strings.TrimSpace(os.Getenv("INTERNAL_OBSERVABILITY_TOKEN"))
	if want == "" {
		return false
	}
	got := r.Header.Get("X-Internal-Token")
	if got == "" {
		got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return got == want
}

func (s *Server) internalRoutes(rt *Router) {
	rt.Handle("GET", "/internal/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !internalAuthorized(r) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
		io.WriteString(w, httpx.Global.Snapshot())
	})
	rt.Handle("GET", "/internal/health/deep", func(w http.ResponseWriter, r *http.Request) {
		if !internalAuthorized(r) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		probe := func(u string) bool {
			req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return false
			}
			res.Body.Close()
			return res.StatusCode/100 == 2 || res.StatusCode == http.StatusNotFound && !strings.HasSuffix(u, "/health")
		}
		checks := map[string]func() bool{
			"postgres":   func() bool { return s.DB.Ping(ctx) == nil },
			"dragonfly":  func() bool { return s.Cache.Ping(ctx) },
			"downloader": func() bool { return probe(strings.TrimRight(s.Cfg.DownloaderURL, "/") + "/health") },
			"token": func() bool {
				base := strings.TrimRight(s.Cfg.YoutubeTokenURL, "/")
				req, _ := http.NewRequestWithContext(ctx, "GET", base+"/ready", nil)
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					return false
				}
				res.Body.Close()
				if res.StatusCode == http.StatusNotFound {
					return probe(base + "/health")
				}
				return res.StatusCode/100 == 2
			},
		}
		out := map[string]string{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for name, fn := range checks {
			wg.Go(func() {
				v := "error"
				if fn() {
					v = "ok"
				}
				mu.Lock()
				out[name] = v
				mu.Unlock()
			})
		}
		wg.Wait()
		status := "ok"
		for _, v := range out {
			if v != "ok" {
				status = "degraded"
			}
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"status": status, "checks": out})
	})
}

var egressReset struct {
	mu   sync.Mutex
	last time.Time
}

// egressResetRoute is YoutubeEgressResetService: after a VPN or proxy rotation, forget what the old address learned
// (token sessions, visitor id, backoffs, idle connections), at most once every two minutes.
func (s *Server) egressResetRoute(rt *Router) {
	rt.Handle("POST", "/internal/provider/youtube/reset", func(w http.ResponseWriter, r *http.Request) {
		want := strings.TrimSpace(os.Getenv("YOUTUBE_EGRESS_RESET_TOKEN"))
		got := r.Header.Get("X-Internal-Token")
		if got == "" {
			if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "Bearer ") {
				got = a[7:]
			}
		}
		if want == "" || strings.TrimSpace(got) != want {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		egressReset.mu.Lock()
		if since := time.Since(egressReset.last); !egressReset.last.IsZero() && since < 2*time.Minute {
			egressReset.mu.Unlock()
			w.Header().Set("Retry-After", strconv.FormatInt(int64((2*time.Minute-since+time.Second-1)/time.Second), 10))
			httpx.Error(w, r, http.StatusTooManyRequests, "YouTube egress reset is in cooldown", "egress_reset_cooldown")
			return
		}
		egressReset.last = time.Now()
		egressReset.mu.Unlock()
		cleared := s.Sabr.Token().Forget() + s.YT.Reset()
		keys := 0
		for _, p := range []string{"youtube-related:v1:*:backoff", "*:v2:*:backoff"} {
			n, _ := s.Cache.DeleteMatching(r.Context(), p)
			keys += n
		}
		s.Log.Warn("youtube egress reset", "clearedFailureEntries", cleared, "clearedBackoffKeys", keys)
		httpx.JSON(w, http.StatusOK, map[string]any{"clearedFailureEntries": cleared, "clearedBackoffKeys": keys, "tokenSessionRecoveryTriggered": false})
	})
}
