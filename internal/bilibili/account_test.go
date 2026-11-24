package bilibili

import (
	"context"
	"testing"
)

func TestAccountCookiesOnlyForBilibiliHTTPS(t *testing.T) {
	ctx := WithAccount(context.Background(), " SESSDATA=x; bili_jct=y ")
	for u, want := range map[string]string{"https://api.bilibili.com/x/player/wbi/playurl?a=1": "SESSDATA=x; bili_jct=y",
		"http://api.bilibili.com/x": "", "https://upos-sz-mirrorcos.bilivideo.com/a.m4s": "", "https://evil.com/?bilibili.com": ""} {
		if got := accountCookies(ctx, u); got != want {
			t.Errorf("%s -> %q", u, got)
		}
	}
	if accountCookies(context.Background(), "https://api.bilibili.com/x") != "" {
		t.Fatal("visitor context carried cookies")
	}
}
