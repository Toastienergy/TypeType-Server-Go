package bilibili

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// Bullet is one danmaku: text, signed ARGB colour, REGULAR/TOP/BOTTOM, font size relative to the player, and the
// moment it appears in milliseconds.
type Bullet struct {
	Text     string
	ARGB     int32
	Position string
	Size     float64
	AtMs     int64
}

// CID resolves a video URL to the cid of its page, which keys its danmaku.
func (c *Client) CID(ctx context.Context, rawURL string) (int64, error) {
	bv := BVID(rawURL)
	if bv == "" {
		return 0, fmt.Errorf("%w: no BV id", ErrUnavailable)
	}
	view, err := c.Get(ctx, "https://api.bilibili.com/x/web-interface/wbi/view?bvid="+bv, rawURL)
	if err != nil {
		return 0, err
	}
	if err := codeErr(view, "view"); err != nil {
		return 0, err
	}
	d := obj(view, "data")
	pages := arr(d, "pages")
	p := 1
	if i := strings.Index(rawURL, "p="); i >= 0 {
		if n, err := strconv.Atoi(strings.SplitN(rawURL[i+2:], "&", 2)[0]); err == nil && n >= 1 && n <= len(pages) {
			p = n
		}
	}
	if len(pages) >= p {
		if cid := num(obj(pages[p-1])["cid"]); cid > 0 {
			return cid, nil
		}
	}
	return num(d["cid"]), nil
}

// Danmaku downloads list.so, which comes raw-deflated (sometimes zlib) XML, and reads every <d> element.
func (c *Client) Danmaku(ctx context.Context, cid int64) ([]Bullet, error) {
	cookies, err := c.visitorCookies(ctx)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.bilibili.com/x/v1/dm/list.so?oid="+strconv.FormatInt(cid, 10), nil)
	c.uaHeaders(req, wwwReferer)
	req.Header.Set("Cookie", cookies)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusPreconditionFailed {
		return nil, ErrRiskControl
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil || res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("bilibili danmaku: HTTP %d %v", res.StatusCode, err)
	}
	body := bytes.TrimLeft(raw, " \t\r\n")
	if len(body) > 0 && body[0] != '<' {
		if body[0] == '{' || body[0] == '[' {
			return nil, fmt.Errorf("bilibili danmaku: unexpected JSON")
		}
		dec, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(raw)), 64<<20))
		if err != nil {
			zr, zerr := zlib.NewReader(bytes.NewReader(raw))
			if zerr != nil {
				return nil, err
			}
			if dec, err = io.ReadAll(io.LimitReader(zr, 64<<20)); err != nil {
				return nil, err
			}
		}
		body = dec
	}
	var doc struct {
		D []struct {
			P    string `xml:"p,attr"`
			Text string `xml:",chardata"`
		} `xml:"d"`
	}
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))))
	dec.Strict = false
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	out := make([]Bullet, 0, len(doc.D))
	for _, d := range doc.D {
		attr := strings.Split(d.P, ",")
		secs, err := strconv.ParseFloat(strings.TrimSpace(attr[0]), 64)
		if err != nil || secs < 0 || math.IsInf(secs, 0) {
			continue
		}
		b := Bullet{Text: d.Text, ARGB: -1, Position: "REGULAR", Size: 0.6, AtMs: int64(math.Round(secs*1000)) + 2500}
		mode := ""
		if len(attr) > 1 {
			mode = strings.TrimSpace(attr[1])
		}
		switch mode {
		case "4":
			b.Position = "BOTTOM"
		case "5":
			b.Position = "TOP"
		case "7":
			var a []any
			if json.Unmarshal([]byte(d.Text), &a) == nil && len(a) > 4 {
				if s, ok := a[4].(string); ok && s != "" {
					b.Text = s
				}
			}
		}
		if len(attr) > 2 {
			switch strings.TrimSpace(attr[2]) {
			case "18":
				b.Size = 0.5
			case "36":
				b.Size = 0.7
			}
		}
		if len(attr) > 3 {
			if rgb, err := strconv.Atoi(strings.TrimSpace(attr[3])); err == nil {
				b.ARGB = int32(uint32(0xFF000000) | uint32(rgb)&0x00FFFFFF)
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// Comments reads one page of a video's comment thread (offset "" for the first page, which also carries the pinned
// ones) and returns the next offset, or "" at the end.
func (c *Client) Comments(ctx context.Context, aid int64, offset string) ([]map[string]any, string, error) {
	pag, _ := json.Marshal(map[string]string{"offset": offset})
	u, err := c.Signed(ctx, "https://api.bilibili.com/x/v2/reply/wbi/main", map[string]string{"oid": strconv.FormatInt(aid, 10), "type": "1",
		"mode": "3", "pagination_str": string(pag), "plat": "1", "web_location": "1315875"})
	if err != nil {
		return nil, "", err
	}
	res, err := c.Get(ctx, u, wwwReferer)
	if err != nil {
		return nil, "", err
	}
	if err := codeErr(res, "comments"); err != nil {
		return nil, "", err
	}
	d := obj(res, "data")
	var out []map[string]any
	if offset == "" {
		for _, x := range arr(d, "top_replies") {
			if o, ok := x.(map[string]any); ok {
				o["isTop"] = true
				out = append(out, o)
			}
		}
	}
	for _, x := range arr(d, "replies") {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	next := ""
	if cur := obj(d, "cursor"); cur["is_end"] != true {
		next = str(obj(cur, "pagination_reply"), "next_offset")
	}
	return out, next, nil
}

// Replies reads one page of the answers to a comment.
func (c *Client) Replies(ctx context.Context, aid, root int64, page int) ([]map[string]any, error) {
	u := "https://api.bilibili.com/x/v2/reply/reply?type=1&ps=10&web_location=333.788&oid=" + strconv.FormatInt(aid, 10) + "&root=" +
		strconv.FormatInt(root, 10) + "&pn=" + strconv.Itoa(page)
	res, err := c.Get(ctx, u, wwwReferer)
	if err != nil {
		return nil, err
	}
	if err := codeErr(res, "replies"); err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, x := range arr(obj(res, "data"), "replies") {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out, nil
}
