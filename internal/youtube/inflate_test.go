package youtube

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/gzip"
)

func TestInflatingTransport(t *testing.T) {
	body := strings.Repeat(`{"videoId":"abc"}`, 500)
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write([]byte(body))
	w.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plain" || r.Header.Get("Accept-Encoding") != "gzip" {
			io.WriteString(w, "plain:"+r.Header.Get("Accept-Encoding"))
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(gz.Bytes())
	}))
	defer srv.Close()
	tr := srv.Client().Transport.(*http.Transport)
	tr.DisableCompression = true
	c := &http.Client{Transport: inflating{tr}}
	get := func(path, ae string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if ae != "" {
			req.Header.Set("Accept-Encoding", ae)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			res, got := get("/gz", "")
			if got != body || res.Header.Get("Content-Encoding") != "" || !res.Uncompressed {
				t.Errorf("gzip answer: %d bytes, headers %v", len(got), res.Header)
			}
		})
	}
	wg.Wait()
	if _, got := get("/plain", ""); got != "plain:gzip" {
		t.Fatalf("plain answer %q", got)
	}
	if _, got := get("/gz", "identity"); got != "plain:identity" {
		t.Fatalf("explicit Accept-Encoding changed: %q", got)
	}
}
