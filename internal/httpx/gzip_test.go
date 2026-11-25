package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/gzip"
)

func serveGzip(h http.HandlerFunc) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	Gzip(h).ServeHTTP(w, req)
	return w
}

func TestGzipCompressesLargeJSONInOneWrite(t *testing.T) {
	body := map[string]string{"text": strings.Repeat("science ", 200)}
	w := serveGzip(func(w http.ResponseWriter, r *http.Request) { JSON(w, http.StatusCreated, body) })
	if w.Code != http.StatusCreated || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d headers %v", w.Code, w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if want := `{"text":"` + strings.Repeat("science ", 200) + `"}`; string(got) != want {
		t.Fatalf("body %q", got[:40])
	}
}

func TestGzipCompressesSmallWritesOnceTheyAddUp(t *testing.T) {
	w := serveGzip(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		for range 30 {
			io.WriteString(w, "0123456789")
		}
	})
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(zr); string(got) != strings.Repeat("0123456789", 30) {
		t.Fatalf("body %q", got)
	}
}

func TestGzipLeavesSmallBodiesAndMediaAlone(t *testing.T) {
	w := serveGzip(func(w http.ResponseWriter, r *http.Request) { JSON(w, http.StatusOK, map[string]string{"status": "UP"}) })
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != `{"status":"UP"}` {
		t.Fatalf("small body %v %q", w.Header(), w.Body.String())
	}
	media := strings.Repeat("m", 1000)
	w = serveGzip(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, media)
	})
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != media {
		t.Fatal("media was compressed")
	}
}

func TestJSONKeepsHTMLCharactersAndReusesBuffersSafely(t *testing.T) {
	for i, v := range []string{strings.Repeat("a<b>&", 400), "short"} {
		w := httptest.NewRecorder()
		JSON(w, http.StatusOK, v)
		if want := `"` + v + `"`; w.Body.String() != want {
			t.Fatalf("answer %d: %q", i, w.Body.String()[:20])
		}
	}
}
