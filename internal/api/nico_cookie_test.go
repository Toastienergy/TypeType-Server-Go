package api

import (
	"slices"
	"testing"
)

func TestNicoBidCookie(t *testing.T) {
	if b := parseNicoCookie("cookie%3Ddomand_bid%3Dabc%3B%20Path%3D%2F"); b == nil || *b != "abc" {
		t.Fatalf("bid %v", b)
	}
	if parseNicoCookie("cookie=other=abc") != nil || parseNicoCookie("x=1") != nil {
		t.Fatal("foreign cookie accepted")
	}
	if b := nicoBid(nil, "https://a.nicovideo.jp/x.m3u8#cookie=dlive_bid=z"); b == nil || *b != "z" {
		t.Fatal("fragment bid")
	}
	if got := nicoBidCookie("https://xyz.dlive.nicovideo.jp/a.m3u8", "z"); got != "dlive_bid=z" {
		t.Fatal(got)
	}
	if got := nicoBidCookie("https://delivery.domand.nicovideo.jp/a.m3u8", "z"); got != "domand_bid=z" {
		t.Fatal(got)
	}
}

func TestUposMirrors(t *testing.T) {
	u := "https://upos-sz-mirrorcos.bilivideo.com/upgcxcode/1/2/3.m4s?e=1&deadline=2"
	got := uposMirrors(u, false, "")
	if len(got) != 3 || got[0] != "https://upos-sz-mirrorali.bilivideo.com/upgcxcode/1/2/3.m4s?e=1&deadline=2" || got[2] != "https://upos-hz-mirrorakam.akamaized.net/upgcxcode/1/2/3.m4s?e=1&deadline=2" {
		t.Fatalf("default %v", got)
	}
	if got := uposMirrors(u, true, "overseas"); slices.ContainsFunc(got, isAkamaiURL) || len(got) != 4 || got[0] != "https://upos-sz-mirroraliov.bilivideo.com/upgcxcode/1/2/3.m4s?e=1&deadline=2" {
		t.Fatalf("overseas %v", got)
	}
	if uposMirrors("https://cn-gdfs-ct-01-01.bilivideo.com/x.m4s", false, "") != nil {
		t.Fatal("non-upos host mirrored")
	}
}
