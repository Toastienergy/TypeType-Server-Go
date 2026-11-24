package api

import (
	"context"
	"io"
	"net/http"
	"os"
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
