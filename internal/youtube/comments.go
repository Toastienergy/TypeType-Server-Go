package youtube

import (
	"context"
	"encoding/base64"
	"errors"
	"html"
	"strings"
	"time"
)

type Comment struct {
	ID                  string  `json:"id"`
	Text                string  `json:"text"`
	Author              string  `json:"author"`
	AuthorURL           string  `json:"authorUrl"`
	AuthorAvatarURL     string  `json:"authorAvatarUrl"`
	LikeCount           int64   `json:"likeCount"`
	TextualLikeCount    string  `json:"textualLikeCount"`
	PublishedTime       string  `json:"publishedTime"`
	PublishedAt         *int64  `json:"publishedAt"`
	IsHeartedByUploader bool    `json:"isHeartedByUploader"`
	IsPinned            bool    `json:"isPinned"`
	UploaderVerified    bool    `json:"uploaderVerified"`
	ReplyCount          int     `json:"replyCount"`
	RepliesPage         *string `json:"repliesPage"`
	Image               any     `json:"image"`
	VoiceReply          any     `json:"voiceReply"`
}

type CommentsPage struct {
	Comments         []Comment `json:"comments"`
	NextPage         *string   `json:"nextpage"`
	CommentsDisabled bool      `json:"commentsDisabled"`
}

// Page tokens are opaque to clients: the innertube continuation, base64url so it survives a query string.
func encodePage(token string) *string {
	s := "yt1." + base64.RawURLEncoding.EncodeToString([]byte(token))
	return &s
}

func decodePage(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, "yt1.")
	if !ok {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	return string(b), err == nil && len(b) > 0
}

var ErrBadPage = errors.New("invalid page")

// Comments returns the first page of a video's comments, or the page a token points to (replies included).
func (c *Client) Comments(ctx context.Context, videoID, page string) (CommentsPage, error) {
	out := CommentsPage{Comments: []Comment{}}
	token := ""
	if page != "" {
		t, ok := decodePage(page)
		if !ok {
			return out, ErrBadPage
		}
		token = t
	} else if token = c.knownComments(videoID); token == "" {
		n, err := c.Call(ctx, Web, "next", map[string]any{"videoId": videoID})
		if err != nil {
			return out, err
		}
		token = commentsToken(n)
		if token == "" {
			out.CommentsDisabled = true
			return out, nil
		}
	}
	res, err := c.Call(ctx, Web, "next", map[string]any{"continuation": token})
	if err != nil {
		return out, err
	}
	parseComments(res, &out, time.Now().UTC())
	return out, nil
}

func commentsToken(n map[string]any) string {
	for _, r := range List(n, "contents", "twoColumnWatchNextResults", "results", "results", "contents") {
		sec := Obj(r, "itemSectionRenderer")
		if Str(sec, "sectionIdentifier") != "comment-item-section" {
			continue
		}
		for _, c := range List(sec, "contents") {
			if t := Str(c, "continuationItemRenderer", "continuationEndpoint", "continuationCommand", "token"); t != "" {
				return t
			}
		}
	}
	return ""
}

func parseComments(res map[string]any, out *CommentsPage, now time.Time) {
	entities := map[string]map[string]any{}
	for _, m := range List(res, "frameworkUpdates", "entityBatchUpdate", "mutations") {
		if k := Str(m, "entityKey"); k != "" {
			entities[k] = Obj(m, "payload")
		}
	}
	var items []any
	for _, ep := range List(res, "onResponseReceivedEndpoints") {
		if a := List(ep, "reloadContinuationItemsCommand", "continuationItems"); a != nil {
			items = append(items, a...)
		}
		if a := List(ep, "appendContinuationItemsAction", "continuationItems"); a != nil {
			items = append(items, a...)
		}
	}
	for _, it := range items {
		if t := Str(it, "continuationItemRenderer", "continuationEndpoint", "continuationCommand", "token"); t != "" {
			out.NextPage = encodePage(t)
			continue
		}
		if t := Str(it, "continuationItemRenderer", "button", "buttonRenderer", "command", "continuationCommand", "token"); t != "" {
			out.NextPage = encodePage(t)
			continue
		}
		vm := Obj(it, "commentThreadRenderer", "commentViewModel", "commentViewModel")
		if vm == nil {
			vm = Obj(it, "commentViewModel")
		}
		if vm == nil {
			continue
		}
		cm, ok := commentFrom(vm, entities, now)
		if !ok {
			continue
		}
		for _, r := range List(it, "commentThreadRenderer", "replies", "commentRepliesRenderer", "contents") {
			if t := Str(r, "continuationItemRenderer", "continuationEndpoint", "continuationCommand", "token"); t != "" {
				cm.RepliesPage = encodePage(t)
			}
		}
		out.Comments = append(out.Comments, cm)
	}
}

func commentFrom(vm map[string]any, entities map[string]map[string]any, now time.Time) (Comment, bool) {
	p := Obj(entities[Str(vm, "commentKey")], "commentEntityPayload")
	if p == nil {
		return Comment{}, false
	}
	props, author, toolbar := Obj(p, "properties"), Obj(p, "author"), Obj(p, "toolbar")
	c := Comment{ID: Str(props, "commentId"), Author: Str(author, "displayName"), PublishedTime: Str(props, "publishedTime")}
	if c.ID == "" {
		c.ID = Str(vm, "commentId")
	}
	c.Text = strings.ReplaceAll(html.EscapeString(Str(props, "content", "content")), "\n", "<br>")
	if id := Str(author, "channelId"); id != "" {
		c.AuthorURL = ChannelURL(id)
	}
	c.AuthorAvatarURL = FixURL(Str(author, "avatarThumbnailUrl"))
	if c.AuthorAvatarURL == "" {
		c.AuthorAvatarURL = LastURL(List(p, "avatar", "image", "sources"))
	}
	c.UploaderVerified = Bool(author, "isVerified") || Bool(author, "isArtist")
	c.TextualLikeCount = Str(toolbar, "likeCountNotliked")
	if n, ok := MixedNumber(c.TextualLikeCount); ok {
		c.LikeCount = n
	}
	if rc := Str(toolbar, "replyCount"); rc != "" {
		if n, ok := MixedNumber(rc); ok {
			c.ReplyCount = int(n)
		}
	}
	if t, ok := TimeAgo(strings.TrimSuffix(c.PublishedTime, " (edited)"), now); ok {
		ms := t.UnixMilli()
		c.PublishedAt = &ms
	}
	if st := Obj(entities[Str(vm, "toolbarStateKey")], "engagementToolbarStateEntityPayload"); st != nil {
		c.IsHeartedByUploader = Str(st, "heartState") == "TOOLBAR_HEART_STATE_HEARTED"
	}
	c.IsPinned = Dig(vm, "pinnedText") != nil
	return c, true
}
