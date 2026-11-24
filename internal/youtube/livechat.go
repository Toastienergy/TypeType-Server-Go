package youtube

import (
	"context"
	"strconv"
	"strings"
)

// ChatMessage is one live chat line, as YoutubeBulletCommentsInfoItemExtractor reads it.
type ChatMessage struct {
	ID, Text, AuthorName, AuthorAvatarURL string
	Moderator                             bool
}

// LiveChatStart reads the first live chat continuation from the watch page data; "" when the video has none.
func (c *Client) LiveChatStart(ctx context.Context, id string) (string, error) {
	res, err := c.Call(ctx, Web, "next", map[string]any{"videoId": id, "contentCheckOk": true, "racyCheckOk": true})
	if err != nil {
		return "", err
	}
	return Str(res, "contents", "twoColumnWatchNextResults", "conversationBar", "liveChatRenderer", "continuations", 0,
		"reloadContinuationData", "continuation"), nil
}

// LiveChatPoll fetches the messages after continuation, plain messages before Super Chats, and the continuation
// to ask next, which is "" when YouTube gave none.
func (c *Client) LiveChatPoll(ctx context.Context, continuation string, offsetMs int64) ([]ChatMessage, string, error) {
	res, err := c.Call(ctx, Web, "live_chat/get_live_chat", map[string]any{"continuation": continuation,
		"currentPlayerState": map[string]any{"playerOffsetMs": strconv.FormatInt(offsetMs, 10)}})
	if err != nil {
		return nil, "", err
	}
	lc := Obj(res, "continuationContents", "liveChatContinuation")
	next := ""
	for _, k := range []string{"timedContinuationData", "invalidationContinuationData"} {
		if v := Str(lc, "continuations", 0, k, "continuation"); v != "" {
			next = v
			break
		}
	}
	var plain, paid []ChatMessage
	for _, a := range List(lc, "actions") {
		item := Obj(a, "addChatItemAction", "item")
		if r := Obj(item, "liveChatTextMessageRenderer"); r != nil {
			plain = append(plain, chatMessage(r, ""))
		} else if r := Obj(item, "liveChatPaidMessageRenderer"); r != nil {
			paid = append(paid, chatMessage(r, Str(r, "purchaseAmountText", "simpleText")))
		}
	}
	return append(plain, paid...), next, nil
}

func chatMessage(r map[string]any, amount string) ChatMessage {
	var b strings.Builder
	for _, run := range List(r, "message", "runs") {
		if t, ok := Obj(run)["text"].(string); ok {
			b.WriteString(t)
		} else if e := Obj(run, "emoji"); e != nil {
			b.WriteString(emojiText(e))
		}
	}
	text := strings.ReplaceAll(b.String(), "□", "")
	if amount != "" && text != "" {
		text = "(" + amount + ") " + text
	}
	m := ChatMessage{ID: Str(r, "id"), Text: text, AuthorName: Text(r["authorName"])}
	if th := List(r, "authorPhoto", "thumbnails"); len(th) > 0 {
		m.AuthorAvatarURL = Str(th[len(th)-1], "url")
	}
	for _, badge := range List(r, "authorBadges") {
		if Str(badge, "liveChatAuthorBadgeRenderer", "icon", "iconType") == "MODERATOR" {
			m.Moderator = true
		}
	}
	return m
}

func emojiText(e map[string]any) string {
	if label := Str(e, "image", "accessibility", "accessibilityData", "label"); label != "" {
		if Bool(e, "isCustomEmoji") {
			return ":" + label + ":"
		}
		return label
	}
	if id := Str(e, "emojiId"); id != "" && !strings.Contains(id, "/") {
		return id
	}
	return Str(e, "shortcuts", 0)
}
