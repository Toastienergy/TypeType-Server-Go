package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Post is CommunityPostItem; Text is the HTML of the post, as PipePipe's Description.
type Post struct {
	ID                  string     `json:"id"`
	URL                 string     `json:"url"`
	Type                string     `json:"type"`
	Text                string     `json:"text"`
	AuthorName          string     `json:"authorName"`
	AuthorURL           string     `json:"authorUrl"`
	AuthorAvatarURL     string     `json:"authorAvatarUrl"`
	PublishedTime       string     `json:"publishedTime"`
	PublishedAt         *int64     `json:"publishedAt"`
	LikeCount           *int64     `json:"likeCount"`
	TextualLikeCount    string     `json:"textualLikeCount"`
	CommentCount        *int64     `json:"commentCount"`
	TextualCommentCount string     `json:"textualCommentCount"`
	Images              []string   `json:"images"`
	Poll                *PostPoll  `json:"poll"`
	Video               *PostVideo `json:"video"`
	SharedPost          *Post      `json:"sharedPost"`
}

type PostPoll struct {
	Type             string       `json:"type"`
	Choices          []PollChoice `json:"choices"`
	VoteCount        *int64       `json:"voteCount"`
	TextualVoteCount string       `json:"textualVoteCount"`
}

type PollChoice struct {
	Text     string  `json:"text"`
	ImageURL *string `json:"imageUrl"`
}

type PostVideo struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	ThumbnailURL string `json:"thumbnailUrl"`
}

type ChannelPosts struct {
	ChannelName string  `json:"channelName"`
	ChannelURL  string  `json:"channelUrl"`
	Posts       []Post  `json:"posts"`
	NextPage    *string `json:"nextpage"`
}

const postsTabParams = "EgVwb3N0c_IGBAoCSgA="

// ChannelPosts is PipePipeChannelPostsService over YoutubeChannelTabExtractor's posts tab.
func (c *Client) ChannelPosts(ctx context.Context, channelURL, page string) (ChannelPosts, error) {
	out := ChannelPosts{ChannelURL: channelURL, Posts: []Post{}}
	var items []any
	if page != "" {
		token, ok := decodePage(page)
		if !ok {
			return out, ErrBadPage
		}
		res, err := c.Call(ctx, Web, "browse", map[string]any{"continuation": token})
		if err != nil {
			return out, err
		}
		items = continuationItems(res, "appendContinuationItemsAction")
		if len(items) == 0 {
			items = continuationItems(res, "reloadContinuationItemsCommand")
		}
		if len(items) == 0 {
			return out, errors.New("could not get YouTube channel tab continuation items")
		}
	} else {
		id, _, err := c.ChannelTarget(ctx, channelURL)
		if err != nil {
			return out, err
		}
		res, err := c.Call(ctx, Web, "browse", map[string]any{"browseId": id, "params": postsTabParams})
		if err != nil {
			return out, err
		}
		items = postsTabItems(res)
	}
	now := time.Now()
	var cont any
	out.Posts, cont = collectPosts(items, now)
	if t := Str(cont, "continuationEndpoint", "continuationCommand", "token"); t != "" {
		out.NextPage = encodePage(t)
	}
	for _, p := range out.Posts {
		if strings.TrimSpace(p.AuthorName) != "" {
			out.ChannelName = p.AuthorName
			break
		}
	}
	return out, nil
}

// continuationItems is addContinuationItemsFrom: every continuationItems of a command, wherever it sits.
func continuationItems(v any, command string) []any {
	var out []any
	switch t := v.(type) {
	case map[string]any:
		out = append(out, List(t, command, "continuationItems")...)
		for _, x := range t {
			out = append(out, continuationItems(x, command)...)
		}
	case []any:
		for _, x := range t {
			out = append(out, continuationItems(x, command)...)
		}
	}
	return out
}

// postsTabItems finds the tab whose URL ends in /posts (or the older /community) and its items, nil when the tab
// is missing or holds only a message.
func postsTabItems(res map[string]any) []any {
	for _, t := range List(res, "contents", "twoColumnBrowseResultsRenderer", "tabs") {
		tr := Obj(t, "tabRenderer")
		if tr == nil {
			tr = Obj(t, "expandableTabRenderer")
		}
		u := Str(tr, "endpoint", "commandMetadata", "webCommandMetadata", "url")
		if u == "" {
			continue
		}
		u, _, _ = strings.Cut(u, "?")
		if !strings.HasSuffix(u, "/posts") && !strings.HasSuffix(u, "/community") {
			continue
		}
		first := List(Dig(tr, "content", "sectionListRenderer", "contents", 0), "itemSectionRenderer", "contents")
		if len(first) == 1 && Obj(first[0], "messageRenderer") != nil {
			return nil
		}
		content := Obj(tr, "content")
		if items := List(Dig(content, "sectionListRenderer", "contents", 0), "itemSectionRenderer", "contents", 0, "gridRenderer", "items"); len(items) > 0 {
			return items
		}
		if items := List(content, "richGridRenderer", "contents"); len(items) > 0 {
			return items
		}
		return List(content, "sectionListRenderer", "contents")
	}
	return nil
}

func collectPosts(items []any, now time.Time) ([]Post, any) {
	var posts []Post
	var cont any
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if t := Obj(m, "backstagePostThreadRenderer"); t != nil {
			m = Obj(t, "post")
		}
		switch {
		case Obj(m, "sharedPostRenderer") != nil:
			if p, ok := parseSharedPost(Obj(m, "sharedPostRenderer"), now); ok {
				posts = append(posts, p)
			}
		case Obj(m, "backstagePostRenderer") != nil || Obj(m, "postRenderer") != nil:
			r := Obj(m, "backstagePostRenderer")
			if r == nil {
				r = Obj(m, "postRenderer")
			}
			if p, ok := parsePost(r, now); ok {
				posts = append(posts, p)
			}
		case Obj(m, "itemSectionRenderer") != nil:
			sub, c := collectPosts(List(m, "itemSectionRenderer", "contents"), now)
			posts = append(posts, sub...)
			if c != nil {
				cont = c
			}
		case Obj(m, "continuationItemRenderer") != nil:
			cont = Obj(m, "continuationItemRenderer")
		}
	}
	if posts == nil {
		posts = []Post{}
	}
	return posts, cont
}

func count(text string) *int64 {
	if text == "" {
		return nil
	}
	if n, ok := MixedNumber(text); ok && n >= 0 {
		return &n
	}
	return nil
}

func postDate(text string, now time.Time) *int64 {
	if text == "" {
		return nil
	}
	if t, ok := TimeAgo(text, now); ok && t.UnixMilli() > 0 {
		ms := t.UnixMilli()
		return &ms
	}
	return nil
}

// parsePost is YoutubeCommunityPostParser.parse.
func parsePost(p map[string]any, now time.Time) (Post, bool) {
	id := Str(p, "postId")
	if id == "" {
		return Post{}, false
	}
	out := Post{ID: id, URL: "https://www.youtube.com/post/" + id, Type: "text", Text: runsHTML(Obj(p, "contentText")),
		AuthorName: plainText(Obj(p, "authorText")), AuthorAvatarURL: largestImage(Obj(p, "authorThumbnail")),
		PublishedTime: plainText(Obj(p, "publishedTimeText")), TextualLikeCount: plainText(Obj(p, "voteCount")), Images: []string{},
		TextualCommentCount: plainText(Dig(p, "actionButtons", "commentActionButtonsRenderer", "replyButton", "buttonRenderer", "text"))}
	if ch := Str(p, "authorEndpoint", "browseEndpoint", "browseId"); ch != "" {
		out.AuthorURL = ChannelURL(ch)
	}
	out.PublishedAt = postDate(out.PublishedTime, now)
	out.LikeCount, out.CommentCount = count(out.TextualLikeCount), count(out.TextualCommentCount)
	att := Obj(p, "backstageAttachment")
	switch {
	case Obj(att, "backstageImageRenderer") != nil:
		out.Type = "image"
		if u := largestImage(Dig(att, "backstageImageRenderer", "image")); u != "" {
			out.Images = append(out.Images, u)
		}
	case Obj(att, "postMultiImageRenderer") != nil:
		out.Type = "image"
		for _, e := range List(att, "postMultiImageRenderer", "images") {
			if u := largestImage(Dig(e, "backstageImageRenderer", "image")); u != "" {
				out.Images = append(out.Images, u)
			}
		}
	case Obj(att, "pollRenderer") != nil:
		out.Type = "poll"
		poll := Obj(att, "pollRenderer")
		pp := &PostPoll{Type: pollType(Str(poll, "type")), Choices: []PollChoice{}, TextualVoteCount: plainText(Obj(poll, "totalVotes"))}
		for _, c := range List(poll, "choices") {
			ch := PollChoice{Text: plainText(Obj(c, "text"))}
			if u := largestImage(Obj(c, "image")); u != "" {
				ch.ImageURL = &u
			}
			pp.Choices = append(pp.Choices, ch)
		}
		pp.VoteCount = count(pp.TextualVoteCount)
		if len(pp.Choices) > 0 {
			out.Poll = pp
		}
	case Obj(att, "videoRenderer") != nil:
		out.Type = "video"
		v := Obj(att, "videoRenderer")
		if id := Str(v, "videoId"); id != "" {
			out.Video = &PostVideo{URL: WatchURL(id), Title: plainText(Obj(v, "title")), ThumbnailURL: largestImage(Obj(v, "thumbnail"))}
		}
	case Obj(att, "sharedPostRenderer") != nil:
		out.Type = "shared_post"
		if r := originalPost(Obj(att, "sharedPostRenderer")); Str(r, "postId") != "" {
			if sp, ok := parsePost(r, now); ok {
				out.SharedPost = &sp
				out.Video = &PostVideo{URL: sp.URL}
			}
		}
	case len(att) > 0:
		out.Type = "unknown"
	}
	return out, true
}

func originalPost(shared map[string]any) map[string]any {
	if r := Obj(shared, "originalPost", "backstagePostRenderer"); r != nil {
		return r
	}
	return Obj(shared, "originalPost", "postRenderer")
}

// parseSharedPost is YoutubeCommunityPostParser.parseShared: a repost, its counts unknown.
func parseSharedPost(p map[string]any, now time.Time) (Post, bool) {
	id := Str(p, "postId")
	if id == "" {
		return Post{}, false
	}
	out := Post{ID: id, URL: "https://www.youtube.com/post/" + id, Type: "shared_post", Text: runsHTML(Obj(p, "content")),
		AuthorName: plainText(Obj(p, "displayName")), AuthorAvatarURL: largestImage(Obj(p, "thumbnail")),
		PublishedTime: plainText(Obj(p, "publishedTimeText")), Images: []string{}}
	if ch := Str(p, "endpoint", "browseEndpoint", "browseId"); ch != "" {
		out.AuthorURL = ChannelURL(ch)
	}
	out.PublishedAt = postDate(out.PublishedTime, now)
	if r := originalPost(p); Str(r, "postId") != "" {
		if sp, ok := parsePost(r, now); ok {
			out.SharedPost = &sp
			out.Video = &PostVideo{URL: sp.URL}
		}
	}
	return out, true
}

func pollType(t string) string {
	switch {
	case strings.Contains(t, "QUIZ"):
		return "quiz"
	case strings.Contains(t, "IMAGE"):
		return "image"
	case strings.Contains(t, "TEXT"):
		return "text"
	}
	return "unknown"
}

// largestImage is largestUrl over getImagesFromThumbnailsArray: the biggest width x height, unknown sizes as -1.
func largestImage(img any) string {
	arr := List(img, "thumbnails")
	if len(arr) == 0 {
		arr = List(img, "sources")
	}
	best, bestArea := "", int64(0)
	for _, t := range arr {
		u := Str(t, "url")
		if strings.TrimSpace(u) == "" {
			continue
		}
		w, h := int64(-1), int64(-1)
		if v, ok := Dig(t, "width").(float64); ok {
			w = int64(v)
		}
		if v, ok := Dig(t, "height").(float64); ok {
			h = int64(v)
		}
		if best == "" || w*h > bestArea {
			best, bestArea = FixURL(u), w*h
		}
	}
	return strings.ReplaceAll(best, "httpss://", "https://")
}

// runText is a run's text, or for an emoji run its first shortcut or its id, as normalizeRuns does.
func runText(run any) string {
	if t, ok := Dig(run, "text").(string); ok {
		return t
	}
	if sc := List(run, "emoji", "shortcuts"); len(sc) > 0 {
		s, _ := sc[0].(string)
		return s
	}
	return Str(run, "emoji", "emojiId")
}

func plainText(obj any) string {
	if s, ok := Dig(obj, "simpleText").(string); ok {
		return s
	}
	var b strings.Builder
	for _, r := range List(obj, "runs") {
		b.WriteString(runText(r))
	}
	return b.String()
}

// runsHTML is getTextFromObject(object, true) after normalizeRuns, with jsoup's Entities.escape.
func runsHTML(obj any) string {
	if s, ok := Dig(obj, "simpleText").(string); ok {
		return strings.ReplaceAll(jsoupEscape(s), "\n", "<br>")
	}
	runs := List(obj, "runs")
	if len(runs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range runs {
		text := jsoupEscape(runText(r))
		if ep := Obj(r, "navigationEndpoint"); ep != nil {
			if u := LinkURL(ep); u != "" {
				text = `<a href="` + u + `">` + text + `</a>`
			}
		}
		bold, italic, strike := Bool(r, "bold"), Bool(r, "italics"), Bool(r, "strikethrough")
		if bold {
			b.WriteString("<b>")
		}
		if italic {
			b.WriteString("<i>")
		}
		if strike {
			b.WriteString("<s>")
		}
		b.WriteString(text)
		if strike {
			b.WriteString("</s>")
		}
		if italic {
			b.WriteString("</i>")
		}
		if bold {
			b.WriteString("</b>")
		}
	}
	s := strings.ReplaceAll(b.String(), "\n", "<br>")
	return strings.ReplaceAll(s, "  ", " &nbsp;")
}

// jsoupEscape is jsoup 1.24's Entities.escape: the five XML characters, the no-break space, and C0 controls other
// than tab, line feed and carriage return as hex references.
func jsoupEscape(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c == '&':
			b.WriteString("&amp;")
		case c == '<':
			b.WriteString("&lt;")
		case c == '>':
			b.WriteString("&gt;")
		case c == '"':
			b.WriteString("&quot;")
		case c == '\'':
			b.WriteString("&apos;")
		case c == 0xA0:
			b.WriteString("&nbsp;")
		case c < 0x20 && c != '\t' && c != '\n' && c != '\r':
			fmt.Fprintf(&b, "&#x%x;", c)
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// LinkURL is getUrlFromNavigationEndpoint: hashtag dialogs, web metadata, redirect links unwrapped, channels,
// playlists and videos.
func LinkURL(ep map[string]any) string {
	if d := Obj(ep, "showDialogCommand"); d != nil {
		item := Dig(d, "panelLoadingStrategy", "inlineContent", "dialogViewModel", "customContent", "listViewModel", "listItems", 0, "listItemViewModel")
		md := Obj(item, "title", "commandRuns", 0, "onTap", "innertubeCommand", "commandMetadata", "webCommandMetadata")
		if len(md) == 0 {
			md = Obj(item, "rendererContext", "commandContext", "onTap", "innertubeCommand", "commandMetadata", "webCommandMetadata")
		}
		if u, ok := md["url"].(string); ok {
			return "https://www.youtube.com" + u
		}
	}
	if u, ok := Dig(ep, "webCommandMetadata", "url").(string); ok {
		return "https://www.youtube.com" + u
	}
	if ue := Obj(ep, "urlEndpoint"); ue != nil {
		u := Str(ue, "url")
		if strings.HasPrefix(u, "https://www.youtube.com/redirect?") {
			u = u[23:]
		}
		if rest, ok := strings.CutPrefix(u, "/redirect?"); ok {
			for _, kv := range strings.Split(rest, "&") {
				if k, v, _ := strings.Cut(kv, "="); k == "q" {
					d, err := url.QueryUnescape(v)
					if err != nil {
						return ""
					}
					return d
				}
			}
		} else if strings.HasPrefix(u, "http") {
			return u
		} else if strings.HasPrefix(u, "/channel") || strings.HasPrefix(u, "/user") || strings.HasPrefix(u, "/watch") {
			return "https://www.youtube.com" + u
		}
	} else if be := Obj(ep, "browseEndpoint"); be != nil {
		id := Str(be, "browseId")
		switch {
		case strings.HasPrefix(id, "UC"):
			return ChannelURL(id)
		case strings.HasPrefix(id, "VL"):
			return "https://www.youtube.com/playlist?list=" + id[2:]
		}
		if base := Str(be, "canonicalBaseUrl"); base != "" {
			return "https://www.youtube.com" + base
		}
	}
	if we := Obj(ep, "watchEndpoint"); we != nil {
		u := WatchURL(Str(we, "videoId"))
		if pl := Str(we, "playlistId"); pl != "" {
			u += "&list=" + pl
		}
		if t, ok := we["startTimeSeconds"].(float64); ok {
			u += fmt.Sprintf("&t=%d", int64(t))
		}
		return u
	}
	if wp := Obj(ep, "watchPlaylistEndpoint"); wp != nil {
		return "https://www.youtube.com/playlist?list=" + Str(wp, "playlistId")
	}
	if u, ok := Dig(ep, "commandMetadata", "webCommandMetadata", "url").(string); ok {
		return "https://www.youtube.com" + u
	}
	return ""
}
