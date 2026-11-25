package api

import (
	"net/http/httptest"
	"testing"
)

func TestTwitchStreamListsPassTheLanguageAndNothingElse(t *testing.T) {
	r := httptest.NewRequest("GET", "/twitch/streams?language=de,en&first=5&evil=1", nil)
	got := pickQuery(r, twitchStreamListParams...)
	if got.Get("language") != "de,en" || got.Get("first") != "5" || got.Has("evil") {
		t.Fatalf("relayed %v", got)
	}
}

func TestTwitchRefusalKeepsItsStatusAndBody(t *testing.T) {
	w := httptest.NewRecorder()
	writeLive(w, liveResponse{status: 400, body: []byte(`{"error":"invalid_language"}`)})
	if w.Code != 400 || w.Body.String() != `{"error":"invalid_language"}` {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
