package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

type countWriter struct {
	h http.Header
	n int
}

func (c *countWriter) Header() http.Header         { return c.h }
func (c *countWriter) Write(p []byte) (int, error) { c.n += len(p); return len(p), nil }
func (c *countWriter) WriteHeader(int)             {}

// BenchmarkCompression runs a feed page and a recorded 780 KB answer through the middleware with each coding.
func BenchmarkCompression(b *testing.B) {
	items := make([]benchItem, 60)
	for i := range items {
		items[i] = benchItem{fmt.Sprintf("vid%08d", i), fmt.Sprintf("A video title number %d about science", i),
			"Some Channel", fmt.Sprintf("https://i.ytimg.com/vi/vid%08d/hqdefault.jpg", i), "12:34", int64(i) * 1234567}
	}
	bodies := map[string]any{"feed": items}
	if raw, err := os.ReadFile(os.Getenv("TEST_NEXT_FIXTURE")); err == nil {
		var tree map[string]any
		json.Unmarshal(raw, &tree)
		bodies["big"] = tree
	}
	for name, body := range bodies {
		for _, coding := range []string{"gzip", "zstd"} {
			b.Run(name+"/"+coding, func(b *testing.B) {
				h := Gzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { JSON(w, http.StatusOK, body) }))
				req := httptest.NewRequest("GET", "/x", nil)
				req.Header.Set("Accept-Encoding", coding)
				var w *countWriter
				b.ReportAllocs()
				for b.Loop() {
					w = &countWriter{h: http.Header{}}
					h.ServeHTTP(w, req)
				}
				b.ReportMetric(float64(w.n), "wire-bytes")
			})
		}
	}
}
