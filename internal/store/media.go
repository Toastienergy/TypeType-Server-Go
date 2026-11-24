package store

import (
	"regexp"
	"strings"
)

var (
	watchIDRe  = regexp.MustCompile(`[?&]v=([A-Za-z0-9_-]{6,})`)
	shortsIDRe = regexp.MustCompile(`(?i)youtube\.com/shorts/([A-Za-z0-9_-]{6,})`)
	liveIDRe   = regexp.MustCompile(`(?i)youtube\.com/live/([A-Za-z0-9_-]{6,})`)
	shortURLRe = regexp.MustCompile(`(?i)youtu\.be/([A-Za-z0-9_-]{6,})`)
	rawIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{6,}$`)
)

// VideoID is YoutubeTypeTypeMapper.videoId: watch, shorts, live and short links, or a bare id.
func VideoID(url string) string {
	t := strings.TrimSpace(url)
	for _, re := range []*regexp.Regexp{watchIDRe, shortsIDRe, liveIDRe, shortURLRe} {
		if m := re.FindStringSubmatch(t); m != nil {
			return m[1]
		}
	}
	if rawIDRe.MatchString(t) {
		return t
	}
	return ""
}

// FallbackTitle and FallbackThumbnail fill what an import or a client left blank, like the Kotlin mapper.
func FallbackTitle(title, url string) string {
	if strings.TrimSpace(title) != "" {
		return title
	}
	if id := VideoID(url); id != "" {
		return "YouTube video " + id
	}
	return url
}

func FallbackThumbnail(thumb, url string) string {
	if strings.TrimSpace(thumb) != "" {
		return thumb
	}
	if id := VideoID(url); id != "" {
		return "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
	}
	return ""
}
