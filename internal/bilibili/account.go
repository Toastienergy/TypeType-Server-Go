package bilibili

import (
	"context"
	"net/url"
	"strings"
)

type accountKey struct{}

// WithAccount makes the Bilibili API calls of ctx carry a signed-in account's cookies instead of the visitor ones,
// as BilibiliCookieContext does for HTTPS requests to bilibili.com: higher qualities and AI subtitles come with it.
func WithAccount(ctx context.Context, cookieHeader string) context.Context {
	if strings.TrimSpace(cookieHeader) == "" {
		return ctx
	}
	return context.WithValue(ctx, accountKey{}, strings.TrimSpace(cookieHeader))
}

func accountCookies(ctx context.Context, rawURL string) string {
	v, _ := ctx.Value(accountKey{}).(string)
	if v == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h != "bilibili.com" && !strings.HasSuffix(h, ".bilibili.com") {
		return ""
	}
	return v
}
