package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloaderRelay(t *testing.T) {
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Disposition", `inline; filename="a.mp4"`)
		io.WriteString(w, "bytes"+r.Header.Get("Range"))
	}))
	defer store.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/artifact"):
			w.Header().Set("X-TypeType-Artifact-Proxy", "1")
			http.Redirect(w, r, store.URL+"/obj", http.StatusFound)
		case r.URL.Path == "/full":
			w.WriteHeader(http.StatusInsufficientStorage)
			io.WriteString(w, `{"details":{"freeBytes":1}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"path":"`+r.URL.Path+`","q":"`+r.URL.RawQuery+`"}`)
		}
	}))
	defer up.Close()
	s := &Server{dl: newDownloader(up.URL), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rt := NewRouter()
	s.downloaderRoutes(rt)
	get := func(path string, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, r)
		return w
	}
	if w := get("/downloader/jobs?x=1"); w.Code != 200 || w.Body.String() != `{"path":"/jobs","q":"x=1"}` {
		t.Fatalf("plain: %d %s", w.Code, w.Body)
	}
	w := get("/downloader/jobs/1/artifact", "Range", "bytes=0-")
	if w.Body.String() != "bytesbytes=0-" || w.Header().Get("Content-Disposition") != `attachment; filename="a.mp4"` ||
		w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("artifact: %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := get("/downloader/jobs/1/artifact?download=0"); w.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("inline artifact: %v", w.Header())
	}
	if w := get("/downloader/full"); w.Code != 507 || !strings.Contains(w.Body.String(), "insufficient_storage") {
		t.Fatalf("storage: %d %s", w.Code, w.Body)
	}
}
