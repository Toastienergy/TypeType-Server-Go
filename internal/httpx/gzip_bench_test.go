package httpx

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type benchItem struct {
	ID, Title, Channel, Thumbnail, Duration string
	Views                                   int64
}

// BenchmarkGzipJSON compresses a feed page of 60 videos through the middleware, the shape of most API answers.
func BenchmarkGzipJSON(b *testing.B) {
	items := make([]benchItem, 60)
	for i := range items {
		items[i] = benchItem{fmt.Sprintf("vid%08d", i), fmt.Sprintf("A video title number %d about science", i),
			"Some Channel", fmt.Sprintf("https://i.ytimg.com/vi/vid%08d/hqdefault.jpg", i), "12:34", int64(i) * 1234567}
	}
	h := Gzip(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { JSON(w, http.StatusOK, items) }))
	req := httptest.NewRequest("GET", "/feed", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	b.ReportAllocs()
	for b.Loop() {
		h.ServeHTTP(discardWriter{http.Header{}}, req)
	}
}

type discardWriter struct{ h http.Header }

func (d discardWriter) Header() http.Header         { return d.h }
func (d discardWriter) Write(p []byte) (int, error) { return io.Discard.Write(p) }
func (d discardWriter) WriteHeader(int)             {}
