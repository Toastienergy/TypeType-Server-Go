package portability

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

var (
	htmlVideoLink  = regexp.MustCompile(`(?i)<a\s+href="([^"]*(?:youtube\.com/watch\?v=|youtube\.com/shorts/|youtube\.com/live/|youtu\.be/)[^"]*)"[^>]*>([^<]*)</a>`)
	htmlAnyLink    = regexp.MustCompile(`(?i)<a\s+href="([^"]+)"[^>]*>([^<]*)</a>`)
	htmlRowTail    = regexp.MustCompile(`(?i)^\s*<br>\s*(?:<a href="([^"]+)">([^<]*)</a><br>\s*)?([^<]*)<br>`)
	watchURLRe     = regexp.MustCompile(`https?://(?:www\.)?(?:youtube\.com/(?:watch\?v=|shorts/|live/)|youtu\.be/)[A-Za-z0-9_-]{6,}`)
	htmlChannelURL = regexp.MustCompile(`https?://www\.youtube\.com/(?:channel/[A-Za-z0-9_-]+|@[A-Za-z0-9._-]+)`)
	wsRe           = regexp.MustCompile(`\s+`)
	entities       = strings.NewReplacer("&nbsp;", " ", "&#39;", "'", "&amp;", "&", "&quot;", `"`, "&lt;", "<", "&gt;", ">")
)

func decodeHTML(v string) string {
	return strings.TrimSpace(wsRe.ReplaceAllString(entities.Replace(tagRe.ReplaceAllString(v, " ")), " "))
}

func decodeText(v string) string {
	return strings.TrimSpace(wsRe.ReplaceAllString(entities.Replace(v), " "))
}

func before(s string, n int) string { return s[max(0, n-160):n] }

// htmlHistory is YoutubeTakeoutHistoryParser.parseWithDiagnostics over one window of an activity page.
func htmlHistory(html string, requireWatched bool, sink Sink) (int64, error) {
	html = strings.ReplaceAll(html, " ", " ")
	links := htmlVideoLink.FindAllStringSubmatchIndex(html, -1)
	var invalid int64
	type key struct {
		u  string
		at int64
	}
	seen := map[key]bool{}
	for i, m := range links {
		if requireWatched && !isWatched(before(html, m[0])) {
			continue
		}
		tail := htmlRowTail.FindStringSubmatchIndex(html[m[1]:])
		if tail == nil {
			continue
		}
		href := decodeHTML(html[m[2]:m[3]])
		u := watchURLRe.FindString(href)
		if u == "" {
			continue
		}
		title := decodeHTML(html[m[4]:m[5]])
		if isUnavailableTitle(title) {
			continue
		}
		next := len(html)
		if i+1 < len(links) {
			next = links[i+1][0]
		}
		group := func(g int) string {
			if tail[2*g] < 0 {
				return ""
			}
			return html[m[1]+tail[2*g] : m[1]+tail[2*g+1]]
		}
		channelURL := group(1)
		hasChannel := strings.TrimSpace(channelURL) != ""
		if isAdEntry(html, m[1]+tail[1], next, hasChannel) {
			continue
		}
		name := decodeHTML(group(2))
		if name == "" {
			name = "Unknown channel"
		}
		at, ok := takeoutDate(decodeHTML(group(3)))
		if !ok {
			invalid++
			continue
		}
		if seen[key{u, at}] {
			continue
		}
		seen[key{u, at}] = true
		v := &Video{URL: u, Title: title, ThumbnailURL: thumbnailFor(u), ChannelName: name, ChannelURL: channelURL, PublishedAt: -1}
		if _, err := sink.Write(&Record{Type: TypeHistory, Video: v, WatchedAt: at}); err != nil {
			return invalid, err
		}
	}
	return invalid, nil
}

// htmlSignals is YoutubeTakeoutActivitySignalService.parseHtml: subscriptions and likes said in an activity page.
func htmlSignals(html string, subscriptionsListed bool, sink Sink) error {
	html = strings.ReplaceAll(html, " ", " ")
	for _, m := range htmlAnyLink.FindAllStringSubmatchIndex(html, -1) {
		prefix := before(html, m[0])
		href, text := decodeText(html[m[2]:m[3]]), decodeText(html[m[4]:m[5]])
		tail := htmlRowTail.FindStringSubmatch(html[m[1]:])
		if ch := htmlChannelURL.FindString(href); ch != "" && isSubscribed(prefix) {
			var at int64
			if tail != nil {
				at, _ = takeoutDate(decodeText(tail[3]))
			}
			ch = strings.Replace(ch, "http://", "https://", 1)
			if subscriptionsListed {
				if err := rememberSubscription(sink, ch, at); err != nil {
					return err
				}
			} else if _, err := sink.Write(&Record{Type: TypeSubscription, ChannelURL: ch, Name: text, SubscribedAt: at}); err != nil {
				return err
			}
		}
		u := watchURLRe.FindString(href)
		if u == "" || !likedRow(prefix, html, m[1]) || isUnavailableTitle(text) || tail == nil {
			continue
		}
		at, ok := takeoutDate(decodeText(tail[3]))
		if !ok {
			continue
		}
		u = strings.Replace(u, "http://", "https://", 1)
		v := &Video{URL: u, Title: firstNonBlank(text, titleFor(u)), ThumbnailURL: thumbnailFor(u), ChannelName: decodeText(tail[2]),
			ChannelURL: strings.Replace(decodeText(tail[1]), "http://", "https://", 1), PublishedAt: -1}
		if _, err := sink.Write(&Record{Type: TypeFavorite, Video: v, FavoritedAt: at}); err != nil {
			return err
		}
	}
	return nil
}

// likedRow reads the verb right before the link, or right after it in languages that put it last, so "You
// disliked" and "No te ha gustado" are not likes.
func likedRow(prefix, html string, after int) bool {
	verb := prefix
	if i := strings.LastIndexByte(verb, '>'); i >= 0 {
		verb = verb[i+1:]
	}
	if verb = decodeText(verb); verb != "" {
		return isLikedAction(verb)
	}
	rest := html[after:min(len(html), after+160)]
	if i := strings.Index(rest, "<br>"); i >= 0 {
		rest = rest[:i]
	}
	rest = decodeText(rest)
	return isLikedAction(rest) || isLikedSuffix(rest)
}

const subscribedAtLookup = "youtube-takeout-subscribed-at"

// rememberSubscription is YoutubeTakeoutSubscriptionDates.remember: the latest subscribe date of a channel.
func rememberSubscription(sink Sink, channel string, at int64) error {
	if at <= 0 || strings.TrimSpace(channel) == "" {
		return nil
	}
	key := canonicalChannel(channel)
	known, err := sink.Lookup(subscribedAtLookup, key)
	if err != nil {
		return err
	}
	if n, _ := parseInt(known); at > n {
		return sink.PutLookup(subscribedAtLookup, key, itoa64(at))
	}
	return nil
}

func subscribedAt(sink Sink, channel string) int64 {
	if strings.TrimSpace(channel) == "" {
		return 0
	}
	v, _ := sink.Lookup(subscribedAtLookup, canonicalChannel(channel))
	n, _ := parseInt(v)
	return n
}

// htmlWindows hands an activity page to fn in windows of about 4 MiB, cut before the last entry so an entry keeps
// its details.
func htmlWindows(r io.Reader, fn func(string) error) error {
	br := bufio.NewReaderSize(r, 32<<10)
	var window strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, err := br.Read(buf)
		window.Write(buf[:n])
		if window.Len() >= 4<<20 {
			s := window.String()
			if i := strings.LastIndex(s, htmlCellStart); i > 0 {
				if err := fn(s[:i]); err != nil {
					return err
				}
				window.Reset()
				window.WriteString(s[i:])
			} else {
				if err := fn(s); err != nil {
					return err
				}
				window.Reset()
				window.WriteString(s[max(0, len(s)-256<<10):])
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if window.Len() > 0 {
		return fn(window.String())
	}
	return nil
}

var (
	jsonVideoURL     = regexp.MustCompile(`(?i)(?:https?://|URLs://)(?:www\.|music\.)?youtube\.com/(?:watch\?(?:[^#\s]*&)?v=|shorts/|live/)[A-Za-z0-9_-]{6,}|https?://youtu\.be/[A-Za-z0-9_-]{6,}`)
	jsonChannelURL   = regexp.MustCompile(`(?i)https?://(?:www\.)?youtube\.com/(?:channel/[A-Za-z0-9_-]+|@[A-Za-z0-9._-]+|c/[A-Za-z0-9._-]+|user/[A-Za-z0-9._-]+)`)
	actionPrefixRe   = regexp.MustCompile(`(?i)^(?:you\s+watched|watched|viewed|you\s+liked|liked|has\s+visto|vous\s+avez\s+regard[eé]|vous\s+avez\s+aime[eé]|te\s+ha\s+gustado|izl[eə]nildi)(?:\s+|$)`)
	actionSuffixRe   = regexp.MustCompile(`(?i)\s+(?:izl[eə]nildi|を視聴しました|を再生しました)\s*$`)
	namedJSONHistory = []string{"watch history json", "watch_history json", "watch history", "myactivity json", "my activity json"}
)

func isTakeoutJSON(p string) bool {
	if !strings.HasSuffix(strings.ToLower(p), ".json") {
		return false
	}
	n := takeoutNorm(p)
	file := n
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		file = n[i+1:]
	}
	named := inSet(file, namedJSONHistory) || strings.HasPrefix(file, "myactivity ") || strings.HasPrefix(file, "my activity ")
	return named || isHistoryPath(p) || strings.Contains(n, "youtube")
}

type jsonSubtitle struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// jsonActivity is one My Activity entry; fields of a nested snippet fill what the entry leaves blank.
type jsonActivity struct {
	Title, TitleURL, Time, Description, Header string
	Controls, Products, Details                []string
	Subtitles                                  []jsonSubtitle
}

func readActivity(m map[string]any) jsonActivity {
	var a jsonActivity
	str := func(k string) string {
		switch v := m[k].(type) {
		case string:
			return v
		case json.Number:
			return v.String()
		}
		return ""
	}
	strs := func(k string) []string {
		var out []string
		for _, x := range asList(m[k]) {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	a.Title, a.TitleURL, a.Description, a.Header = str("title"), str("titleUrl"), str("description"), str("header")
	a.Time = firstNonBlank(str("time"), str("publishedAt"))
	if str("time") != "" {
		a.Time = str("time")
	}
	a.Controls, a.Products = strs("activityControls"), strs("products")
	for _, x := range asList(m["subtitles"]) {
		if o, ok := x.(map[string]any); ok {
			n, _ := o["name"].(string)
			u, _ := o["url"].(string)
			a.Subtitles = append(a.Subtitles, jsonSubtitle{n, u})
		}
	}
	for _, x := range asList(m["details"]) {
		if o, ok := x.(map[string]any); ok {
			if n, ok := o["name"].(string); ok {
				a.Details = append(a.Details, n)
			}
		}
	}
	if s, ok := m["snippet"].(map[string]any); ok {
		n := readActivity(s)
		a.Title, a.TitleURL, a.Time = firstNonBlank(a.Title, n.Title), firstNonBlank(a.TitleURL, n.TitleURL), firstNonBlank(a.Time, n.Time)
		a.Description, a.Header = firstNonBlank(a.Description, n.Description), firstNonBlank(a.Header, n.Header)
		a.Controls, a.Products = append(a.Controls, n.Controls...), append(a.Products, n.Products...)
		a.Subtitles, a.Details = append(a.Subtitles, n.Subtitles...), append(a.Details, n.Details...)
	}
	return a
}

func findVideoURL(v string) string {
	u := jsonVideoURL.FindString(v)
	switch {
	case u == "":
		return ""
	case strings.HasPrefix(strings.ToLower(u), "urls://"), strings.HasPrefix(strings.ToLower(u), "http://"):
		return "https://" + u[strings.Index(u, "://")+3:]
	}
	return u
}

func (a jsonActivity) cleanTitle(s string) string {
	return strings.TrimSpace(actionSuffixRe.ReplaceAllString(strings.TrimSpace(actionPrefixRe.ReplaceAllString(s, "")), ""))
}

func (a jsonActivity) channel() *jsonSubtitle {
	for _, s := range a.Subtitles {
		if jsonChannelURL.MatchString(s.URL) {
			c := s
			return &c
		}
	}
	u := jsonChannelURL.FindString(a.TitleURL)
	if u == "" {
		return nil
	}
	return &jsonSubtitle{a.cleanTitle(a.Title), u}
}

func activityID(u string) string {
	if i := strings.Index(u, "v="); i >= 0 {
		id := u[i+2:]
		if j := strings.IndexByte(id, '&'); j >= 0 {
			id = id[:j]
		}
		if id != "" {
			return id
		}
	}
	id := u[strings.LastIndexByte(u, '/')+1:]
	if j := strings.IndexByte(id, '?'); j >= 0 {
		id = id[:j]
	}
	return id
}

func (a jsonActivity) video(u string) *Video {
	title := a.cleanTitle(strings.TrimSpace(jsonVideoURL.ReplaceAllString(a.Title, "")))
	if title == "" {
		title = "YouTube video " + activityID(u)
	}
	v := &Video{URL: u, Title: title, ThumbnailURL: "https://i.ytimg.com/vi/" + activityID(u) + "/hqdefault.jpg", PublishedAt: -1}
	if c := a.channel(); c != nil {
		v.ChannelName, v.ChannelURL = c.Name, c.URL
	}
	return v
}

func (a jsonActivity) isAd() bool {
	for _, d := range a.Details {
		if isAdDetail(d) {
			return true
		}
	}
	hasChannel := false
	for _, s := range a.Subtitles {
		hasChannel = hasChannel || strings.TrimSpace(s.URL) != ""
	}
	if len(a.Details) > 0 && !hasChannel {
		return true
	}
	n := takeoutNorm(a.Title)
	return strings.HasPrefix(n, "viewed ads on youtube") || strings.HasPrefix(n, "watched ads on youtube")
}

func (a jsonActivity) isYoutube() bool {
	markers := append(append([]string{}, a.Products...), a.Controls...)
	if strings.TrimSpace(a.Header) != "" {
		markers = append(markers, a.Header)
	}
	if len(markers) == 0 {
		return true
	}
	for _, m := range markers {
		if strings.Contains(takeoutNorm(m), "youtube") {
			return true
		}
	}
	return false
}

func anyOf(list []string, f func(string) bool) bool {
	for _, x := range list {
		if f(x) {
			return true
		}
	}
	return false
}

func (a jsonActivity) importHistory(direct, embedded string) bool {
	n := takeoutNorm(a.Title)
	if strings.HasPrefix(n, "visited ") || strings.HasPrefix(n, "searched ") || strings.HasPrefix(n, "viewed a post") || strings.HasPrefix(n, "viewed ads") {
		return false
	}
	if isWatchedAction(a.Title) || anyOf(a.Controls, isHistoryPath) {
		return true
	}
	if isLikedAction(a.Title) || isSubscribedAction(a.Title) || anyOf(a.Controls, isLiked) || anyOf(a.Controls, isSubscribed) {
		return false
	}
	return direct != "" && strings.TrimSpace(a.Header) == "" && len(a.Products) == 0 && len(a.Controls) == 0 && embedded == ""
}

// jsonActivities is YoutubeTakeoutJsonPortabilityReader.read over one My Activity JSON file.
func jsonActivities(r io.Reader, sink Sink, subscriptionsListed bool) error {
	d := json.NewDecoder(bufio.NewReaderSize(r, 64<<10))
	d.UseNumber()
	t, err := d.Token()
	if err != nil || t != json.Delim('[') {
		return nil
	}
	count := 0
	var invalidDates, history int64
	for d.More() {
		count++
		if count > MaxContainerRecords {
			return invalid("YouTube Takeout JSON contains too many records")
		}
		var v any
		if err := d.Decode(&v); err != nil {
			return invalid("Invalid YouTube Takeout JSON")
		}
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		a := readActivity(m)
		if a.isAd() || !a.isYoutube() {
			continue
		}
		direct := findVideoURL(a.TitleURL)
		embedded := firstNonBlank(findVideoURL(a.Title), findVideoURL(a.Description))
		u := firstNonBlank(direct, embedded)
		var at int64
		dated := false
		if strings.TrimSpace(a.Time) != "" {
			at, dated = takeoutDate(a.Time)
		}
		isHistory := u != "" && a.importHistory(direct, embedded)
		liked := isLikedAction(a.Title) || anyOf(a.Controls, isLiked)
		switch {
		case u != "" && dated && isHistory:
			vid := a.video(u)
			if _, err := sink.Write(&Record{Type: TypeHistory, Video: vid, WatchedAt: at}); err != nil {
				return err
			}
			history++
			if liked {
				if _, err := sink.Write(&Record{Type: TypeFavorite, Video: vid, FavoritedAt: at}); err != nil {
					return err
				}
			}
		case u != "" && dated && liked:
			if _, err := sink.Write(&Record{Type: TypeFavorite, Video: a.video(u), FavoritedAt: at}); err != nil {
				return err
			}
		case u != "" && isHistory:
			invalidDates++
		}
		if isSubscribedAction(a.Title) || anyOf(a.Controls, isSubscribed) {
			if c := a.channel(); c != nil {
				if subscriptionsListed {
					if err := rememberSubscription(sink, c.URL, at); err != nil {
						return err
					}
				} else if _, err := sink.Write(&Record{Type: TypeSubscription, ChannelURL: c.URL, Name: c.Name, SubscribedAt: at}); err != nil {
					return err
				}
			}
		}
	}
	if history > 0 {
		if err := sink.MarkCategory(History); err != nil {
			return err
		}
	}
	if invalidDates > 0 {
		return sink.Issue(Issue{History, "invalid_takeout_date", "YouTube Takeout JSON rows with an invalid date were skipped", invalidDates})
	}
	return nil
}
