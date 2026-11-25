package niconico

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

var embeddedData = regexp.MustCompile(`<script[^>]*id="embedded-data"[^>]*data-props="([^"]*)"`)

// LiveSearch is NiconicoSearchExtractor for the "lives" content filter: the programmes on air from the live site's
// search page, and whether it has another page.
func (c *Client) LiveSearch(ctx context.Context, keyword string, page int) ([]map[string]any, bool, error) {
	u := "https://live.nicovideo.jp/search?keyword=" + url.QueryEscape(keyword) + "&page=" + strconv.Itoa(page)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	req.Header.Set("Accept-Language", "ja-JP")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, false, fmt.Errorf("%w: live search HTTP %d", ErrUnavailable, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	m := embeddedData.FindSubmatch(body)
	if m == nil {
		return nil, false, errors.New("niconico live search: no embedded data")
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &root); err != nil {
		return nil, false, err
	}
	onAir, _ := dig(root, "searchResult", "statusData", "onair").(map[string]any)
	programs, _ := onAir["programs"].([]any)
	out := make([]map[string]any, 0, len(programs))
	for _, p := range programs {
		if o, ok := p.(map[string]any); ok {
			out = append(out, o)
		}
	}
	more, _ := onAir["hasMore"].(bool)
	return out, more, nil
}

// ListSearch is NiconicoSearchExtractor for the "playlists" content filter: public mylists, ten a page, sorted by
// sortQuery ("&sortKey=...").
func (c *Client) ListSearch(ctx context.Context, keyword, sortQuery string, page int) ([]map[string]any, error) {
	d, err := c.nvapi(ctx, "https://nvapi.nicovideo.jp/v1/search/list?_frontendId=6&_frontendVersion=0&keyword="+url.QueryEscape(keyword)+
		sortQuery+"&types=mylist&pageSize=10&page="+strconv.Itoa(page))
	if err != nil {
		return nil, err
	}
	items, _ := d["items"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, x := range items {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out, nil
}
