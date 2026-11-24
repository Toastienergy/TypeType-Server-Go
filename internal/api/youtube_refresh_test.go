package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMergeYoutubeCookies(t *testing.T) {
	got := mergeYoutubeCookies("SID=a; HSID=b; LOGIN_INFO=c", []string{"HSID=new; Path=/; Secure", "LOGIN_INFO=; Expires=Thu, 01 Jan 1970", "TRACKER=x; Path=/"})
	if got != "SID=a; HSID=new" {
		t.Fatalf("%q", got)
	}
}

func TestRefreshYoutubeCookies(t *testing.T) {
	for _, c := range []struct {
		handler func(w http.ResponseWriter)
		want    youtubeRefresh
	}{
		{func(w http.ResponseWriter) { w.Header().Add("Set-Cookie", "TRACKER=x"); w.WriteHeader(200) }, ytRefreshNotRequired},
		{func(w http.ResponseWriter) { w.Header().Add("Set-Cookie", "SID=rotated; Path=/"); w.WriteHeader(200) }, ytRefreshed},
		{func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://accounts.google.com/ServiceLogin")
			w.WriteHeader(303)
		}, ytRefreshReconnect},
		{func(w http.ResponseWriter) { w.WriteHeader(503) }, ytRefreshRetry},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Cookie") != "SID=a" {
				w.WriteHeader(400)
				return
			}
			c.handler(w)
		}))
		old := youtubeSubscriptionsURL
		youtubeSubscriptionsURL = srv.URL
		res, merged := refreshYoutubeCookies(context.Background(), "SID=a")
		youtubeSubscriptionsURL = old
		srv.Close()
		if res != c.want || res == ytRefreshed && merged != "SID=rotated" {
			t.Fatalf("got %v %q want %v", res, merged, c.want)
		}
	}
}
