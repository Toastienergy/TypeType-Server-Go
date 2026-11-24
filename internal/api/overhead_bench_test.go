package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"typetype.dev/server/internal/httpx"
)

// BenchmarkOverhead measures what every request pays before and after its handler: routing through the full
// catalog trie, request id, CORS, gzip negotiation and the access log, on a handler that writes a small JSON body.
func BenchmarkOverhead(b *testing.B) {
	rt := NewRouter()
	for _, c := range catalog {
		rt.Handle(c[0], c[1], func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, http.StatusOK, map[string]string{"status": "UP"}) })
	}
	h := httpx.Observe(slog.New(slog.NewTextHandler(io.Discard, nil)), httpx.CORS(nil, true, httpx.Gzip(rt.Handler())))
	for _, path := range []string{"/health", "/channel/playlists", "/admin/users/abc/allowed/channels/https://www.youtube.com/@x"} {
		b.Run(path[:min(len(path), 24)], func(b *testing.B) {
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("Origin", "https://example.test")
			if path[1] == 'a' {
				req = httptest.NewRequest("DELETE", path, nil)
			}
			b.ReportAllocs()
			for b.Loop() {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
			}
		})
	}
}
