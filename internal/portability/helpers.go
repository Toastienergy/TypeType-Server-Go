package portability

import (
	"regexp"
	"strings"
)

func youtubeChannelURL(v string) string {
	t := strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(t, "http://"), strings.HasPrefix(t, "https://"):
		return t
	case t != "":
		return "https://www.youtube.com/channel/" + t
	}
	return ""
}

func youtubeVideoURL(v string) string {
	t := strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(t, "http://"), strings.HasPrefix(t, "https://"):
		return t
	case t != "":
		return "https://www.youtube.com/watch?v=" + t
	}
	return ""
}

// youtubeID is the part after the last slash and the last equals sign, up to a query or fragment.
func youtubeID(v string) string {
	if i := strings.LastIndexByte(v, '/'); i >= 0 {
		v = v[i+1:]
	}
	if i := strings.LastIndexByte(v, '='); i >= 0 {
		v = v[i+1:]
	}
	if i := strings.IndexAny(v, "&?#"); i >= 0 {
		v = v[:i]
	}
	return v
}

var xmlAttr = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

var opmlChannelID = regexp.MustCompile(`[?&]channel_id=([^&#]+)`)
