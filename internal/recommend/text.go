package recommend

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

func normalizeToken(token string) string {
	if strings.TrimSpace(token) == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(token)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var topicStopWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true,
	"your": true, "into": true, "about": true, "how": true, "what": true, "when": true, "sur": true, "les": true, "des": true, "une": true,
	"pour": true, "avec": true, "dans": true, "que": true, "qui": true, "est": true, "tout": true, "plus": true, "sans": true}

// Tokenize is RecommendationTopicTokenizer: distinct normalized words of three letters or more, in order.
func Tokenize(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		t := normalizeToken(f)
		if len(t) >= 3 && !topicStopWords[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func semanticKey(title string) string {
	seen := map[string]bool{}
	var keep []string
	for _, f := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
		if len(f) >= 4 && !seen[f] {
			seen[f] = true
			if keep = append(keep, f); len(keep) == 3 {
				break
			}
		}
	}
	return strings.Join(keep, "|")
}

func topicPairs(title string) []string {
	tokens := Tokenize(title)
	sort.Strings(tokens)
	if len(tokens) > 4 {
		tokens = tokens[:4]
	}
	var pairs []string
	for i := range tokens {
		for j := i + 1; j < len(tokens); j++ {
			pairs = append(pairs, tokens[i]+"|"+tokens[j])
		}
	}
	return pairs
}

var (
	liveMarkers  = []string{" live", "is live", "\U0001F534", "en direct", "direct:", "livestream", "live:", " stream", "streamed"}
	eventMarkers = []string{" vs ", " versus ", "full match", "full game", "playoff", "semi-final", "quarter-final", "final \U0001F3C6",
		"tournament", "championship", "main stream", " esports", "marathon", "night of ", "khotba", "prayer", " sermon", " show", " event",
		" concert", " awards"}
	yearRe     = regexp.MustCompile(`\b202[4-6]\b`)
	dayRe      = regexp.MustCompile(`(?i)\bday\s+\d+\b`)
	dateRe     = regexp.MustCompile(`\b\d{1,2}[-/]\d{1,2}[-/]202[4-6]\b`)
	multiDayRe = regexp.MustCompile(`(?i)\b\d+\s+(days?|nights?)\b`)
)

// LiveLike is HomeRecommendationLiveTitleDetector: titles that read like a stream or a dated event recording.
func LiveLike(title string) bool {
	lower := strings.ToLower(title)
	for _, m := range liveMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	day := dayRe.MatchString(title)
	if yearRe.MatchString(title) && (day || dateRe.MatchString(title)) || multiDayRe.MatchString(title) {
		return true
	}
	events := 0
	for _, m := range eventMarkers {
		if strings.Contains(lower, m) {
			events++
		}
	}
	return day && events > 0 || events >= 2
}

// ServiceID maps a video URL to its PipePipe service: Bilibili 5, NicoNico 6, otherwise YouTube 0.
func ServiceID(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	h := strings.ToLower(u.Hostname())
	switch {
	case h == "b23.tv" || h == "bilibili.com" || strings.HasSuffix(h, ".bilibili.com"):
		return 5
	case h == "nico.ms" || h == "nicovideo.jp" || strings.HasSuffix(h, ".nicovideo.jp"):
		return 6
	}
	return 0
}
