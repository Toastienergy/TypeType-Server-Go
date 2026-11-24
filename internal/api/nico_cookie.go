package api

import (
	"net/url"
	"strings"
)

// parseNicoCookie reads the bid cookie the NicoNico extractor appends as "#cookie=domand_bid=..." to a stream URL.
func parseNicoCookie(fragment string) *string {
	decoded, err := url.QueryUnescape(fragment)
	if err != nil {
		return nil
	}
	for _, part := range strings.Split(decoded, "&") {
		c, ok := strings.CutPrefix(part, "cookie=")
		if !ok {
			continue
		}
		name, value, found := strings.Cut(c, "=")
		if !found || name != "domand_bid" && name != "dlive_bid" {
			return nil
		}
		v, _, _ := strings.Cut(value, ";")
		v = strings.TrimSpace(v)
		return &v
	}
	return nil
}

// nicoBidCookie names the cookie after the CDN: lives are served by dlive and read dlive_bid, videos by domand.
func nicoBidCookie(target, bid string) string {
	host := ""
	if u, err := url.Parse(target); err == nil {
		host = u.Hostname()
	}
	if host == "dlive.nicovideo.jp" || strings.HasSuffix(host, ".dlive.nicovideo.jp") {
		return "dlive_bid=" + bid
	}
	return "domand_bid=" + bid
}

// nicoBid is the bid of a proxied URL: the domand_bid parameter first, then the cookie of the URL fragment.
func nicoBid(param *string, raw string) *string {
	if param != nil {
		return param
	}
	if _, frag, ok := strings.Cut(raw, "#"); ok && strings.TrimSpace(frag) != "" {
		return parseNicoCookie(frag)
	}
	return nil
}
