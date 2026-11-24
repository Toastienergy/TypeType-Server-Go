package youtube

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTVPlayerRequest(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"playabilityStatus":{"status":"OK"}}`))
	}))
	defer srv.Close()
	old := tvPlayerURL
	tvPlayerURL = srv.URL
	defer func() { tvPlayerURL = old }()
	c := &Client{http: srv.Client()}
	p, err := c.TVPlayer(context.Background(), "acc", "vid", "vis", "room", "pot", "cpn", 20779001)
	if err != nil || Str(p, "playabilityStatus", "status") != "OK" {
		t.Fatal(err)
	}
	if hdr.Get("Authorization") != "Bearer acc" || hdr.Get("X-Youtube-Client-Name") != "7" || hdr.Get("X-Goog-Visitor-Id") != "vis" {
		t.Fatalf("headers %v", hdr)
	}
	if Str(got, "context", "client", "tvAppInfo", "livingRoomPoTokenId") != "room" || Str(got, "serviceIntegrityDimensions", "poToken") != "pot" {
		t.Fatalf("body %v", got)
	}
	if n, _ := Num(got, "playbackContext", "contentPlaybackContext", "signatureTimestamp"); n != 20779001 {
		t.Fatalf("sts %v", n)
	}
}

func TestAccountAuthorization(t *testing.T) {
	a := Account{Cookies: "SAPISID=abc; __Secure-3PAPISID=def; SID=x"}
	h := a.authorization(timeAt(1700000000))
	if h != "SAPISIDHASH 1700000000_"+sha1Hex("1700000000 abc https://www.youtube.com")+" SAPISID3PHASH 1700000000_"+sha1Hex("1700000000 def https://www.youtube.com") {
		t.Fatalf("%s", h)
	}
}

func timeAt(sec int64) time.Time { return time.Unix(sec, 0) }

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
