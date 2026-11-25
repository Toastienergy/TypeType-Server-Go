package httpx

import (
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
)

const gzipMinSize = 200

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return w
}}

// gzipWriter decides at the first write: compress only text-like bodies of at least gzipMinSize bytes, never media,
// event streams, HLS playlists or 429s, like the Kotlin Compression config.
type gzipWriter struct {
	http.ResponseWriter
	status  int
	decided bool
	gz      *gzip.Writer
	buf     []byte
}

func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(gw, r)
		gw.finish()
	})
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.decided {
		return
	}
	g.status = code
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified || g.Header().Get("Content-Length") != "" && len(g.buf) == 0 {
		g.decide(false)
	}
}

func (g *gzipWriter) compressible() bool {
	h := g.Header()
	if g.status == http.StatusTooManyRequests || h.Get("Content-Encoding") != "" {
		return false
	}
	ct := strings.ToLower(h.Get("Content-Type"))
	switch {
	case ct == "",
		strings.HasPrefix(ct, "image/"), strings.HasPrefix(ct, "video/"), strings.HasPrefix(ct, "audio/"),
		strings.HasPrefix(ct, "application/octet-stream"), strings.HasPrefix(ct, "text/event-stream"),
		strings.HasPrefix(ct, "application/vnd.apple.mpegurl"):
		return false
	}
	return true
}

func (g *gzipWriter) decide(compress bool) {
	g.decided = true
	if compress {
		h := g.Header()
		h.Del("Content-Length")
		h["Content-Encoding"] = gzipEncoding
		g.gz = gzipPool.Get().(*gzip.Writer)
		g.gz.Reset(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(g.status)
	if len(g.buf) > 0 {
		g.write(g.buf)
		g.buf = nil
	}
}

func (g *gzipWriter) write(b []byte) (int, error) {
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if g.decided {
		return g.write(b)
	}
	if !g.compressible() {
		g.decide(false)
		return g.write(b)
	}
	if len(g.buf)+len(b) < gzipMinSize {
		g.buf = append(g.buf, b...)
		return len(b), nil
	}
	g.decide(true)
	return g.write(b)
}

func (g *gzipWriter) Flush() {
	if !g.decided {
		g.decide(len(g.buf) > 0 && g.compressible())
	}
	if g.gz != nil {
		g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) finish() {
	if !g.decided {
		g.decide(false)
	}
	if g.gz != nil {
		g.gz.Close()
		g.gz.Reset(nil)
		gzipPool.Put(g.gz)
		g.gz = nil
	}
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
