package niconico

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/websocket"
)

var (
	ErrLiveFollowers  = errors.New("The live is for followers only")
	ErrLivePrivate    = errors.New("タイムシフト非公開番組です")
	ErrLivePaid       = errors.New("Only available for premium users")
	ErrLiveNotStarted = errors.New("The live is not started yet")
	ErrLiveNoURL      = errors.New("Failed to get live url")
)

// Live is NiconicoStreamExtractor's live branch: the program, and the HLS address the watch session hands out with
// its dlive_bid cookie in the fragment, as "#cookie=...".
type Live struct {
	ID, Title, Description, Thumbnail, SupplierName, SupplierURL, AvatarURL string
	WatchCount                                                              int64
	BeginMs                                                                 int64
	HLS                                                                     string
}

func IsLiveURL(raw string) bool { return strings.Contains(raw, "live.nicovideo.jp") }

const liveUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/108.0.0.0 Safari/537.36"

// Live reads the program page, then opens its watch websocket for at most ten seconds to get the stream address.
func (c *Client) Live(ctx context.Context, rawURL string) (*Live, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", liveUA)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	res.Body.Close()
	if err != nil {
		return nil, err
	}
	page := string(body)
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, err
	}
	var props struct {
		Site struct {
			Relive struct {
				WebSocketURL string `json:"webSocketUrl"`
			} `json:"relive"`
		} `json:"site"`
		Program struct {
			NicoliveProgramID string `json:"nicoliveProgramId"`
			Title             string `json:"title"`
			Description       string `json:"description"`
			BeginTime         int64  `json:"beginTime"`
			Supplier          struct {
				Name    string `json:"name"`
				PageURL string `json:"pageUrl"`
			} `json:"supplier"`
			Thumbnail struct {
				Large string `json:"large"`
				Huge  struct {
					S1280 string `json:"s1280x720"`
				} `json:"huge"`
			} `json:"thumbnail"`
			Statistics struct {
				WatchCount int64 `json:"watchCount"`
			} `json:"statistics"`
		} `json:"program"`
	}
	raw := attrOf(doc, func(n *html.Node) bool { return n.Data == "script" && attr(n, "id") == "embedded-data" }, "data-props")
	if raw == "" || json.Unmarshal([]byte(raw), &props) != nil {
		return nil, ErrLiveNoURL
	}
	p := props.Program
	live := &Live{ID: p.NicoliveProgramID, Title: p.Title, Description: p.Description, SupplierName: p.Supplier.Name, SupplierURL: p.Supplier.PageURL,
		WatchCount: p.Statistics.WatchCount, BeginMs: p.BeginTime * 1000}
	switch {
	case p.Thumbnail.Huge.S1280 != "":
		live.Thumbnail = p.Thumbnail.Huge.S1280
	case p.Thumbnail.Large != "":
		live.Thumbnail = p.Thumbnail.Large
	default:
		og := attrOf(doc, func(n *html.Node) bool { return n.Data == "meta" && attr(n, "property") == "og:image" }, "content")
		if d, err := url.QueryUnescape(og); err == nil {
			og = d
		}
		live.Thumbnail = og
	}
	live.AvatarURL = avatarOf(doc)
	if ws := props.Site.Relive.WebSocketURL; ws != "" {
		if hls, cookie := watchSession(ctx, ws); hls != "" {
			if cookie != "" {
				hls += "#cookie=" + cookie
			}
			live.HLS = hls
			return live, nil
		}
	}
	switch {
	case strings.Contains(page, "フォロワー限定"):
		return nil, ErrLiveFollowers
	case strings.Contains(page, "非公開"):
		return nil, ErrLivePrivate
	case strings.Contains(page, "会員無料"):
		return nil, ErrLivePaid
	case live.BeginMs > time.Now().UnixMilli():
		return nil, ErrLiveNotStarted
	}
	return nil, ErrLiveNoURL
}

// watchSession is NicoWebSocketClient: startWatching, answer the pings, and keep the stream address with its
// cookies once the stream and the message server are both announced.
func watchSession(ctx context.Context, wsURL string) (hls, cookie string) {
	cfg, err := websocket.NewConfig(wsURL, "https://live.nicovideo.jp")
	if err != nil {
		return "", ""
	}
	for k, v := range map[string]string{"Pragma": "no-cache", "Accept-Language": "en-US,en;q=0.9", "User-Agent": liveUA, "Cache-Control": "no-cache"} {
		cfg.Header.Set(k, v)
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := cfg.DialContext(dctx)
	if err != nil {
		return "", ""
	}
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	conn.SetDeadline(deadline)
	start := `{"type":"startWatching","data":{"stream":{"quality":"abr","protocol":"hls","latency":"high","accessRightMethod":"single_cookie",` +
		`"chasePlay":false},"room":{"protocol":"webSocket","commentable":true},"reconnect":false}}`
	if websocket.Message.Send(conn, start) != nil {
		return "", ""
	}
	server := ""
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var msg string
		if websocket.Message.Receive(conn, &msg) != nil {
			return "", ""
		}
		if msg == `{"type":"ping"}` {
			websocket.Message.Send(conn, `{"type":"pong"}`)
			websocket.Message.Send(conn, `{"type":"keepSeat"}`)
			continue
		}
		var m struct {
			Type string `json:"type"`
			Data struct {
				URI     string `json:"uri"`
				ViewURI string `json:"viewUri"`
				Cookies []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"cookies"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(msg), &m) != nil {
			continue
		}
		switch m.Type {
		case "stream":
			var parts []string
			for _, c := range m.Data.Cookies {
				if c.Name != "" && c.Value != "" {
					parts = append(parts, c.Name+"="+c.Value)
				}
			}
			hls, cookie = m.Data.URI, strings.Join(parts, "; ")
		case "messageServer":
			server = m.Data.ViewURI
		}
		if hls != "" && server != "" {
			return hls, cookie
		}
	}
	return "", ""
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func attrOf(n *html.Node, match func(*html.Node) bool, key string) string {
	if n.Type == html.ElementNode && match(n) {
		return attr(n, key)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if v := attrOf(c, match, key); v != "" {
			return v
		}
	}
	return ""
}

// avatarOf is the "div.thumbnail-area > a > img" source of the supplier's icon.
func avatarOf(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "div" && strings.Contains(" "+attr(n, "class")+" ", " thumbnail-area ") {
		for a := n.FirstChild; a != nil; a = a.NextSibling {
			if a.Type == html.ElementNode && a.Data == "a" {
				for img := a.FirstChild; img != nil; img = img.NextSibling {
					if img.Type == html.ElementNode && img.Data == "img" {
						return attr(img, "src")
					}
				}
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if v := avatarOf(c); v != "" {
			return v
		}
	}
	return ""
}
