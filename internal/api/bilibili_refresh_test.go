package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshCsrfAndCorrespondPath(t *testing.T) {
	if got := refreshCsrf(`<html><body><div id="0-name">x</div><div id="1-name"> b0cc8411ded2f9db </div></body></html>`); got != "b0cc8411ded2f9db" {
		t.Fatalf("csrf %q", got)
	}
	if got := refreshCsrf(`<div id="1-name">bad value!</div>`); got != "" {
		t.Fatalf("invalid csrf accepted: %q", got)
	}
	p, err := correspondPath(1700000000000)
	if err != nil || len(p) != 256 {
		t.Fatalf("path %d %v", len(p), err)
	}
}

func fakePassport(t *testing.T, infoCode string, confirmed *string) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cookie/info"):
			w.Write([]byte(`{"code":` + infoCode + `,"data":{"refresh":true,"timestamp":1700000000000}}`))
		case strings.HasPrefix(r.URL.Path, "/correspond/1/"):
			w.Write([]byte(`<div id="1-name">csrf123</div>`))
		case strings.HasSuffix(r.URL.Path, "/cookie/refresh"):
			r.ParseForm()
			if r.Form.Get("csrf") != "jct-old" || r.Form.Get("refresh_csrf") != "csrf123" || r.Form.Get("refresh_token") != "tok-old" {
				w.Write([]byte(`{"code":-400}`))
				return
			}
			w.Header().Add("Set-Cookie", "SESSDATA=new,1800000000,x; Path=/; HttpOnly")
			http.SetCookie(w, &http.Cookie{Name: "bili_jct", Value: "jct-new"})
			w.Write([]byte(`{"code":0,"data":{"refresh_token":"tok-new"}}`))
		case strings.HasSuffix(r.URL.Path, "/confirm/refresh"):
			r.ParseForm()
			*confirmed = r.Form.Get("csrf") + "|" + r.Form.Get("refresh_token")
			w.Write([]byte(`{"code":0}`))
		}
	}))
	t.Cleanup(srv.Close)
	oldP, oldC, oldW := biliPassport, biliCorrespond, biliRefreshWeb
	biliPassport, biliCorrespond, biliRefreshWeb = srv.URL+"/x/passport-login/web", srv.URL+"/correspond/1", srv.Client()
	t.Cleanup(func() { biliPassport, biliCorrespond, biliRefreshWeb = oldP, oldC, oldW })
}

func TestBiliRotateAndConfirm(t *testing.T) {
	var confirmed string
	fakePassport(t, "0", &confirmed)
	old := "SESSDATA=old; bili_jct=jct-old; DedeUserID=7; other=drop"
	res, cookies, token, expires := biliRotate(context.Background(), old, "tok-old")
	if res != refreshRotated || token != "tok-new" || expires != 1800000000000 {
		t.Fatalf("rotate %v %q %d", res, token, expires)
	}
	if cookies != "SESSDATA=new,1800000000,x; bili_jct=jct-new; DedeUserID=7" {
		t.Fatalf("cookies %q", cookies)
	}
	if biliConfirm(context.Background(), cookies, "tok-old") != refreshConfirmed || confirmed != "jct-new|tok-old" {
		t.Fatalf("confirm %q", confirmed)
	}
}

func TestBiliRotateSignedOut(t *testing.T) {
	var confirmed string
	fakePassport(t, "-101", &confirmed)
	if res, _, _, _ := biliRotate(context.Background(), "SESSDATA=a; bili_jct=b", "t"); res != refreshReconnect {
		t.Fatalf("got %v", res)
	}
}
