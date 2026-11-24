// Package niconico reads NicoNico videos the way PipePipe's NiconicoStreamExtractor does: the watch page's
// server-response data, an HLS access right that sets the domand_bid cookie, and the master playlist it unlocks.
package niconico

import (
	"bytes"
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
	"strings"
	"time"
)

const firefoxUA = "Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/119.0"

var (
	ErrUnavailable = errors.New("niconico video unavailable")
	IDRe           = regexp.MustCompile(`(?i)\b((?:sm|so|nm)[0-9]+)\b`)
	serverResp     = regexp.MustCompile(`<meta name="server-response" content="([^"]*)"`)
	audioURIRe     = regexp.MustCompile(`#EXT-X-MEDIA:TYPE=AUDIO[^\n]*?URI="([^"]+)"`)
	kbpsRe         = regexp.MustCompile(`audio-[a-z0-9]+-(\d+)kbps`)
	resRe          = regexp.MustCompile(`video-[a-z0-9]+-(\d+p)`)
)

type Client struct{ http *http.Client }

func NewClient(h *http.Client) *Client {
	if h == nil {
		h = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{http: h}
}

type Variant struct {
	URL        string
	Resolution string
	Kbps       int64
}

type Video struct {
	ID, Title, Description, Thumbnail string
	OwnerName, OwnerURL, OwnerIcon    string
	Views, Likes, Duration            int64
	Registered                        time.Time
	Tags                              []string
	Bid                               string
	Videos, Audios                    []Variant
}

func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func s(v any, keys ...string) string { x, _ := dig(v, keys...).(string); return x }
func n(v any, keys ...string) int64  { x, _ := dig(v, keys...).(float64); return int64(x) }

func (c *Client) watchData(ctx context.Context, id string) (map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://www.nicovideo.jp/watch/"+id, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	req.Header.Set("Accept-Language", "ja-JP")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	m := serverResp.FindSubmatch(body)
	if m == nil {
		switch {
		case bytes.Contains(body, []byte("チャンネル会員専用動画")):
			return nil, fmt.Errorf("%w: channel members only", ErrUnavailable)
		case bytes.Contains(body, []byte("地域と同じ地域からのみ視聴")):
			return nil, fmt.Errorf("%w: region restricted", ErrUnavailable)
		}
		return nil, fmt.Errorf("%w: login required or removed", ErrUnavailable)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &root); err != nil {
		return nil, err
	}
	resp, _ := dig(root, "data", "response").(map[string]any)
	if v4, ok := resp["$watchV4"].(map[string]any); ok {
		resp, _ = v4["data"].(map[string]any)
	}
	if resp == nil {
		return nil, fmt.Errorf("%w: no watch data", ErrUnavailable)
	}
	if s(resp, "errorCode") == "FORBIDDEN" {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, s(resp, "reasonCode"))
	}
	if s(resp, "okReason") == "PAYMENT_PREVIEW_SUPPORTED" {
		return nil, fmt.Errorf("%w: paid content", ErrUnavailable)
	}
	return resp, nil
}

// Watch loads a video and the HLS variants its access right unlocks; every variant needs the returned bid cookie.
func (c *Client) Watch(ctx context.Context, id string) (*Video, error) {
	w, err := c.watchData(ctx, id)
	if err != nil {
		return nil, err
	}
	v := &Video{ID: id, Title: s(w, "video", "title"), Description: s(w, "video", "description"), Views: n(w, "video", "count", "view"),
		Likes: n(w, "video", "count", "like"), Duration: n(w, "video", "duration")}
	v.Thumbnail = s(w, "video", "thumbnail", "ogp")
	if v.Thumbnail == "" {
		v.Thumbnail = s(w, "video", "thumbnail", "url")
	}
	if t, err := time.Parse(time.RFC3339, s(w, "video", "registeredAt")); err == nil {
		v.Registered = t
	}
	v.OwnerName, v.OwnerURL, v.OwnerIcon = uploader(w)
	tags, _ := dig(w, "tag", "items").([]any)
	for _, t := range tags {
		if name := s(t, "name"); name != "" {
			v.Tags = append(v.Tags, name)
		}
	}
	media, _ := dig(w, "media").(map[string]any)
	contents, _ := media["contents"].(map[string]any)
	if contents == nil {
		contents, _ = media["domand"].(map[string]any)
	}
	key := s(media, "accessRightKey")
	if key == "" {
		key = s(media, "domand", "accessRightKey")
	}
	var videos []string
	audio := ""
	for _, x := range func() []any { a, _ := contents["videos"].([]any); return a }() {
		if ok, _ := dig(x, "isAvailable").(bool); ok {
			videos = append(videos, s(x, "id"))
		}
	}
	for _, x := range func() []any { a, _ := contents["audios"].([]any); return a }() {
		if ok, _ := dig(x, "isAvailable").(bool); ok && audio == "" {
			audio = s(x, "id")
		}
	}
	if audio == "" || key == "" {
		return nil, fmt.Errorf("%w: no available stream", ErrUnavailable)
	}
	outputs := make([][2]string, len(videos))
	for i, vid := range videos {
		outputs[i] = [2]string{vid, audio}
	}
	body, _ := json.Marshal(map[string]any{"outputs": outputs})
	track := s(w, "client", "watchTrackId")
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://nvapi.nicovideo.jp/v1/watch/"+id+"/access-rights/hls?actionTrackId="+track, bytes.NewReader(body))
	for k, val := range map[string]string{"User-Agent": firefoxUA, "Accept": "*/*", "Accept-Language": "en-US,en;q=0.5", "Referer": "https://www.nicovideo.jp/",
		"Content-Type": "application/json", "X-Request-With": "https://www.nicovideo.jp", "X-Access-Right-Key": key, "X-Frontend-Id": "6",
		"X-Frontend-Version": "0"} {
		req.Header.Set(k, val)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	var rights struct {
		Data struct {
			ContentURL string `json:"contentUrl"`
		} `json:"data"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&rights)
	res.Body.Close()
	if res.StatusCode/100 != 2 || err != nil || rights.Data.ContentURL == "" {
		return nil, fmt.Errorf("niconico access rights: HTTP %d", res.StatusCode)
	}
	for _, ck := range res.Cookies() {
		if ck.Name == "domand_bid" {
			v.Bid = ck.Value
		}
	}
	mreq, _ := http.NewRequestWithContext(ctx, "GET", rights.Data.ContentURL, nil)
	mreq.Header.Set("User-Agent", firefoxUA)
	mreq.Header.Set("Referer", "https://www.nicovideo.jp/")
	mreq.Header.Set("Origin", "https://www.nicovideo.jp")
	if v.Bid != "" {
		mreq.Header.Set("Cookie", "domand_bid="+v.Bid)
	}
	mres, err := c.http.Do(mreq)
	if err != nil {
		return nil, err
	}
	master, err := io.ReadAll(io.LimitReader(mres.Body, 4<<20))
	mres.Body.Close()
	if mres.StatusCode/100 != 2 || err != nil {
		return nil, fmt.Errorf("niconico master playlist: HTTP %d", mres.StatusCode)
	}
	for _, m := range audioURIRe.FindAllStringSubmatch(string(master), -1) {
		a := Variant{URL: m[1]}
		if k := kbpsRe.FindStringSubmatch(m[1]); k != nil {
			a.Kbps, _ = strconv.ParseInt(k[1], 10, 64)
		}
		v.Audios = append(v.Audios, a)
	}
	for _, line := range strings.Split(string(master), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "https://") && strings.Contains(t, ".m3u8") {
			vv := Variant{URL: t}
			if r := resRe.FindStringSubmatch(t); r != nil {
				vv.Resolution = r[1]
			}
			v.Videos = append(v.Videos, vv)
		}
	}
	return v, nil
}

// Related is the watch page's recommendation list.
func (c *Client) Related(ctx context.Context, id string) ([]map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://nvapi.nicovideo.jp/v1/recommend?recipeId=video_watch_recommendation&site=nicovideo&_frontendId=6&_frontendVersion=0&videoId="+id, nil)
	req.Header.Set("Accept-Language", "ja-JP")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data.Items, nil
}

// Trending is the 24-hour all-genre ranking, read from the ranking page's server-response data.
func (c *Client) Trending(ctx context.Context) ([]map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://www.nicovideo.jp/ranking/genre/all?term=24h", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	m := serverResp.FindSubmatch(body)
	if m == nil {
		return nil, nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &root); err != nil {
		return nil, err
	}
	items, _ := dig(root, "data", "response", "$getTeibanRanking", "data", "items").([]any)
	out := make([]map[string]any, 0, len(items))
	for _, x := range items {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// Search reads one page of video results from the search page's server-response data; sortQuery defaults to
// PipePipe's sort=h&order=d.
func (c *Client) Search(ctx context.Context, keyword, sortQuery string, page int) ([]map[string]any, error) {
	if sortQuery == "" {
		sortQuery = "sort=h&order=d"
	}
	u := "https://www.nicovideo.jp/search/" + url.PathEscape(keyword) + "?" + sortQuery + "&page=" + strconv.Itoa(page)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "ja-JP")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	m := serverResp.FindSubmatch(body)
	if m == nil {
		return nil, errors.New("niconico search: no server-response")
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &root); err != nil {
		return nil, err
	}
	items, _ := dig(root, "data", "response", "$getSearchVideoV2", "data", "items").([]any)
	out := make([]map[string]any, 0, len(items))
	for _, x := range items {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// Bullet is one comment drawn over the video.
type Bullet struct {
	Text     string
	ARGB     int32
	Position string
	Size     float64
	AtMs     int64
}

var (
	nicoColors = map[string]uint32{"white": 0xFFFFFF, "red": 0xFF0000, "pink": 0xFF8080, "orange": 0xFFC000, "yellow": 0xFFFF00, "green": 0x00FF00,
		"cyan": 0x00FFFF, "blue": 0x0000FF, "purple": 0xC000FF, "black": 0x000000, "white2": 0xCCCCCC, "niconicoWhite": 0xCCCC99, "red2": 0xCC0033,
		"truered": 0xCC0033, "pink2": 0xFF33CC, "orange2": 0xFF6600, "passionorange": 0xFF7F00, "yellow2": 0x999900, "madyellow": 0x999900,
		"green2": 0x00CC66, "elementalgreen": 0x00CC66, "cyan2": 0x00CCCC, "blue2": 0x3399FF, "marineblue": 0x3399FF, "purple2": 0x6633FF,
		"nobleviolet": 0x6633FF, "black2": 0x666666}
	nicoPositions = map[string]string{"top": "TOP", "bottom": "BOTTOM", "ue": "TOP", "shita": "BOTTOM"}
	nicoSizes     = map[string]float64{"small": 0.5, "big": 0.7}
)

// Comments posts the watch page's nvComment ticket to the comment server and flattens every thread, newest last
// reversed as NiconicoCommentsCache does.
func (c *Client) Comments(ctx context.Context, id string) ([]Bullet, error) {
	w, err := c.watchData(ctx, id)
	if err != nil {
		return nil, err
	}
	ticket, err := json.Marshal(dig(w, "comment", "nvComment"))
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://public.nvcomment.nicovideo.jp/v1/threads", bytes.NewReader(ticket))
	req.Header.Set("x-frontend-id", "6")
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Data struct {
			Threads []struct {
				Comments []map[string]any `json:"comments"`
			} `json:"threads"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(&out); err != nil {
		return nil, err
	}
	var all []Bullet
	for _, t := range out.Data.Threads {
		for _, cm := range t.Comments {
			b := Bullet{Text: s(cm, "body"), ARGB: -1, Position: "REGULAR", Size: 0.7}
			if b.Text == "" {
				b.Text = s(cm, "content")
			}
			b.Text = strings.TrimPrefix(b.Text, "/emotion ")
			var styles []string
			if mail := s(cm, "mail"); mail != "" {
				styles = strings.Split(mail, " ")
			} else if cmds, ok := cm["commands"].([]any); ok {
				for _, x := range cmds {
					if v, ok := x.(string); ok {
						styles = append(styles, v)
					}
				}
			}
			colour, pos, size := false, false, false
			for _, st := range styles {
				if c, ok := nicoColors[st]; ok && !colour {
					b.ARGB, colour = int32(0xFF000000|c), true
				}
				if p, ok := nicoPositions[st]; ok && !pos {
					b.Position, pos = p, true
				}
				if z, ok := nicoSizes[st]; ok && !size {
					b.Size, size = z, true
				}
			}
			if _, ok := cm["vpos"]; ok {
				b.AtMs = n(cm, "vpos") * 10
			} else {
				b.AtMs = n(cm, "vposMs")
			}
			all = append(all, b)
		}
	}
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all, nil
}

func (c *Client) nvapi(ctx context.Context, u string) (map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("X-Frontend-Id", "6")
	req.Header.Set("X-Frontend-Version", "0")
	req.Header.Set("X-Niconico-Language", "ja-jp")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%w: nvapi HTTP %d", ErrUnavailable, res.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	d, _ := out["data"].(map[string]any)
	return d, nil
}

// User reads a user's profile and one page (100 per page) of their uploads, newest first.
func (c *Client) User(ctx context.Context, id string, page int) (profile map[string]any, items []map[string]any, total int64, err error) {
	prof, err := c.nvapi(ctx, "https://nvapi.nicovideo.jp/v1/users/"+id)
	if err != nil {
		return nil, nil, 0, err
	}
	vids, err := c.nvapi(ctx, fmt.Sprintf("https://nvapi.nicovideo.jp/v3/users/%s/videos?sortKey=registeredAt&sortOrder=desc&sensitiveContents=mask&pageSize=100&page=%d", id, page))
	if err != nil {
		return nil, nil, 0, err
	}
	profile, _ = prof["user"].(map[string]any)
	list, _ := vids["items"].([]any)
	for _, x := range list {
		if e, ok := dig(x, "essential").(map[string]any); ok {
			items = append(items, e)
		}
	}
	return profile, items, n(vids, "totalCount"), nil
}

// uploader is NiconicoStreamExtractor's uploader: since October 2026 the page's structured data
// (metadata.jsonLd.owner) carries it, the owner and channel objects of the watch data before that.
func uploader(w map[string]any) (name, pageURL, icon string) {
	ld, _ := dig(w, "metadata", "jsonLd", "owner").(map[string]any)
	owner, _ := dig(w, "owner").(map[string]any)
	ch, _ := dig(w, "channel").(map[string]any)
	channel := owner == nil
	if _, ok := w["metadata"]; ok {
		channel = s(ld, "type") == "channel"
	}
	id := s(ld, "id")
	if id == "" {
		if channel {
			id = s(ch, "id")
		} else if n := n(owner, "id"); n > 0 {
			id = strconv.FormatInt(n, 10)
		} else {
			id = s(owner, "id")
		}
	}
	name = s(ld, "name")
	if name == "" {
		if channel {
			name = s(ch, "name")
		} else {
			name = s(owner, "nickname")
		}
	}
	if name == "" {
		name = "Unknown"
	}
	if channel {
		pageURL = "https://ch.nicovideo.jp/" + id
	} else {
		pageURL = "https://www.nicovideo.jp/user/" + id
	}
	icon = s(ld, "iconUrl")
	if _, has := ld["iconUrl"]; !has {
		if channel {
			icon = s(ch, "thumbnail", "url")
		} else {
			icon = s(owner, "iconUrl")
		}
	}
	return name, pageURL, icon
}
