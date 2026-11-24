package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterPrecedence(t *testing.T) {
	rt := NewRouter()
	hit := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(name + "|" + r.PathValue("videoId") + r.PathValue("sessionId") + "|" + r.PathValue("videoUrl") + r.PathValue("code")))
		}
	}
	rt.Handle("GET", "/sabr/{videoId}/{itag}/segment/{seq}", hit("segment"))
	rt.Handle("GET", "/sabr/playback/{sessionId}/{itag}/init", hit("init"))
	rt.Handle("GET", "/progress/batch", hit("batch"))
	rt.Handle("GET", "/progress/{videoUrl...}", hit("progress"))
	rt.Handle("GET", "/avatar/openmoji/{code}.svg", hit("emoji"))
	cases := map[string]string{
		"/sabr/playback/abc/251/init":       "init|abc|",
		"/sabr/vid/251/segment/3":           "segment|vid|",
		"/sabr/playback/251/segment/3":      "segment|playback|",
		"/progress/batch":                   "batch||",
		"/progress/https%3A%2F%2Fy.com%2Fw": "progress||https://y.com/w",
		"/progress/https://y.com/watch":     "progress||https://y.com/watch",
		"/avatar/openmoji/1F600.svg":        "emoji||1F600",
	}
	for path, want := range cases {
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if got := w.Body.String(); got != want {
			t.Errorf("%s: got %q want %q", path, got, want)
		}
	}
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, httptest.NewRequest("POST", "/progress/batch", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
}
