package db

import (
	"net/url"
	"strings"
)

// CanonicalChannelURL matches ChannelUrlCanonicalizer: https, lowercase host, default ports dropped, trailing
// slashes, query and fragment removed. Anything java.net.URI would reject is returned trimmed and unchanged.
func CanonicalChannelURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsAny(trimmed, " \"<>\\^`{|}") {
		return trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return trimmed
	}
	var b strings.Builder
	b.WriteString("https://")
	if u.User != nil {
		b.WriteString(u.User.String())
		b.WriteByte('@')
	}
	if strings.Contains(host, ":") {
		b.WriteString("[" + host + "]")
	} else {
		b.WriteString(host)
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		b.WriteString(":" + p)
	}
	path := u.EscapedPath()
	for len(path) > 1 && strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}
	if path != "/" {
		b.WriteString(path)
	}
	return b.String()
}
