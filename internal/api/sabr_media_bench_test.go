package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type nullWriter struct{ h http.Header }

func (n *nullWriter) Header() http.Header         { return n.h }
func (n *nullWriter) Write(p []byte) (int, error) { return len(p), nil }
func (n *nullWriter) WriteHeader(int)             {}

func BenchmarkWriteMedia(b *testing.B) {
	data := make([]byte, 512<<10)
	req := httptest.NewRequest("GET", "/sabr/playback/x/media/137/12", nil)
	b.ReportAllocs()
	for b.Loop() {
		writeMedia(&nullWriter{http.Header{}}, req, containerMime("video/mp4; codecs=\"avc1.640028\""), data)
	}
}

func TestWriteMediaHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	writeMedia(w, httptest.NewRequest("GET", "/x", nil), "audio/webm", []byte("abc"))
	h := w.Header()
	if h.Get("Content-Type") != "audio/webm" || h.Get("Cache-Control") != "no-store" || h.Get("Accept-Ranges") != "bytes" ||
		h.Get("Content-Length") != "3" || w.Body.String() != "abc" {
		t.Fatalf("headers %v body %q", h, w.Body.String())
	}
}
