package portability

import (
	"regexp"
	"strconv"
	"strings"
	"typetype.dev/server/internal/db"
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

func itoa(i int) string { return strconv.Itoa(i) }

var (
	watchIDRe  = regexp.MustCompile(`[?&]v=([A-Za-z0-9_-]{6,})`)
	shortsIDRe = regexp.MustCompile(`(?i)youtube\.com/shorts/([A-Za-z0-9_-]{6,})`)
	liveIDRe   = regexp.MustCompile(`(?i)youtube\.com/live/([A-Za-z0-9_-]{6,})`)
	shortURLRe = regexp.MustCompile(`(?i)youtu\.be/([A-Za-z0-9_-]{6,})`)
	rawIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{6,}$`)
)

// videoIDOf is YoutubeTypeTypeMapper.videoId.
func videoIDOf(u string) string {
	t := strings.TrimSpace(u)
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

func thumbnailFor(u string) string {
	if id := videoIDOf(u); id != "" {
		return "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
	}
	return ""
}

func titleFor(u string) string {
	if id := videoIDOf(u); id != "" {
		return "YouTube video " + id
	}
	return u
}

func canonicalChannel(u string) string { return db.CanonicalChannelURL(u) }

func parseInt(s string) (int64, error) { return strconv.ParseInt(strings.TrimSpace(s), 10, 64) }

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
