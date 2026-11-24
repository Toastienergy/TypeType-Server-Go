package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/bilibili"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

type biliCommentPage struct {
	Aid    int64  `json:"a"`
	Offset string `json:"o,omitempty"`
	Root   int64  `json:"r,omitempty"`
	Page   int    `json:"p,omitempty"`
}

func encodeBiliComments(p biliCommentPage) *string {
	b, _ := json.Marshal(p)
	s := "bili1." + base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func biliComment(o map[string]any, aid int64) youtube.Comment {
	m, _ := o["member"].(map[string]any)
	content, _ := o["content"].(map[string]any)
	num := func(m map[string]any, k string) int64 { f, _ := m[k].(float64); return int64(f) }
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	text := html.UnescapeString(str(content, "message"))
	if jump, ok := content["jump_url"].(map[string]any); ok && strings.HasSuffix(text, "...") {
		for k := range jump {
			if strings.HasPrefix(k, "https://") {
				text += "\n\n" + k
				break
			}
		}
	}
	likes := num(o, "like")
	c := youtube.Comment{ID: str(o, "rpid_str"), Text: text, Author: str(m, "uname"), AuthorURL: "https://space.bilibili.com/" + strconv.FormatInt(num(o, "mid"), 10),
		AuthorAvatarURL: https(str(m, "avatar")), LikeCount: likes, TextualLikeCount: strconv.FormatInt(likes, 10), ReplyCount: int(num(o, "rcount"))}
	if up, ok := o["up_action"].(map[string]any); ok {
		c.IsHeartedByUploader, _ = up["like"].(bool)
	}
	c.IsPinned, _ = o["isTop"].(bool)
	if ct := num(o, "ctime"); ct > 0 {
		c.PublishedTime = time.Unix(ct, 0).UTC().Format("2006-01-02 15:04:05")
		at := ct * 1000
		c.PublishedAt = &at
	}
	root, parent, rpid := num(o, "root"), num(o, "parent"), num(o, "rpid")
	if replies, _ := o["replies"].([]any); len(replies) > 0 && !(root == parent && root == rpid) {
		c.RepliesPage = encodeBiliComments(biliCommentPage{Aid: aid, Root: rpid, Page: 1})
	}
	return c
}

func (s *Server) bilibiliComments(ctx context.Context, w http.ResponseWriter, r *http.Request, raw, page string) {
	out := youtube.CommentsPage{Comments: []youtube.Comment{}}
	var pg biliCommentPage
	if page != "" {
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(page, "bili1."))
		if !strings.HasPrefix(page, "bili1.") || err != nil || json.Unmarshal(b, &pg) != nil || pg.Aid <= 0 {
			plain(w, r, http.StatusBadRequest, "Invalid page")
			return
		}
	} else {
		bv := bilibili.BVID(raw)
		if bv == "" {
			plain(w, r, http.StatusBadRequest, "Unsupported comments URL")
			return
		}
		pg.Aid = bilibili.BV2AV(bv)
	}
	var items []map[string]any
	var err error
	if pg.Root > 0 {
		items, err = s.bili.Replies(ctx, pg.Aid, pg.Root, pg.Page)
		if err == nil && !(len(items) < 9 && pg.Page == 1) && len(items) > 0 {
			out.NextPage = encodeBiliComments(biliCommentPage{Aid: pg.Aid, Root: pg.Root, Page: pg.Page + 1})
		}
	} else {
		var next string
		items, next, err = s.bili.Comments(ctx, pg.Aid, pg.Offset)
		if err == nil && next != "" && len(items) > 0 {
			out.NextPage = encodeBiliComments(biliCommentPage{Aid: pg.Aid, Offset: next})
		}
	}
	if err != nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not load comments", "error")
		return
	}
	for _, o := range items {
		out.Comments = append(out.Comments, biliComment(o, pg.Aid))
	}
	httpx.JSON(w, http.StatusOK, out)
}
