package niconico

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ChannelInfo is what a ch.nicovideo.jp page says of its channel.
type ChannelInfo struct {
	Name, Description, AvatarURL, ID string
}

// ChannelVideo is one item of a channel's video feed, read as NiconicoTrendRSSExtractor reads it.
type ChannelVideo struct {
	Title, URL, ThumbnailURL, UploadDate string
	Duration, ViewCount, Uploaded        int64
	Premium                              bool
}

var (
	metaDescription = regexp.MustCompile(`<meta name="description" content="([^"]*)"`)
	metaSiteName    = regexp.MustCompile(`<meta property="og:site_name" content="([^"]*)"`)
	metaImage       = regexp.MustCompile(`<meta property="og:image" content="([^"]*)"`)
	pageTitle       = regexp.MustCompile(`(?s)<title>(.*?)</title>`)
	channelID       = regexp.MustCompile(`channel_id\s*=\s*'(ch\d+)'`)
	watchLink       = regexp.MustCompile(`(nicovideo\.jp/watch|nico\.ms)/(.+)?`)
	rssThumbnail    = regexp.MustCompile(`(?s)class="nico-thumbnail".*?<img[^>]*src="([^"]*)"`)
	rssLength       = regexp.MustCompile(`class="nico-info-length">([^<]*)<`)
	rssViews        = regexp.MustCompile(`class="nico-info-total-view">([^<]*)<`)
	rssDate         = regexp.MustCompile(`class="nico-info-date">([^<]*)<`)
)

// Channel is NiconicoUserExtractor for a channel: the channel page's description, name and icon, and one page of
// its video feed (/video?rss=2.0).
func (c *Client) Channel(ctx context.Context, channelURL string, page int) (ChannelInfo, []ChannelVideo, error) {
	feed, err := c.page(ctx, channelURL+"/video?rss=2.0&page="+strconv.Itoa(page))
	if err != nil {
		return ChannelInfo{}, nil, err
	}
	body, err := c.page(ctx, channelURL)
	if err != nil {
		return ChannelInfo{}, nil, err
	}
	first := func(re *regexp.Regexp, s string) string {
		if m := re.FindStringSubmatch(s); m != nil {
			return html.UnescapeString(m[1])
		}
		return ""
	}
	info := ChannelInfo{Description: first(metaDescription, body), Name: first(metaSiteName, body), AvatarURL: first(metaImage, body),
		ID: first(channelID, body)}
	if info.Name == "" {
		info.Name = strings.TrimSpace(first(pageTitle, body))
	}
	var rss struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			Premium     string `xml:"isPremium"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal([]byte(feed), &rss); err != nil {
		return ChannelInfo{}, nil, err
	}
	var videos []ChannelVideo
	for _, it := range rss.Items {
		link := strings.TrimSpace(it.Link)
		if !watchLink.MatchString(link) {
			continue
		}
		d := it.Description
		v := ChannelVideo{Title: it.Title, URL: link, ThumbnailURL: first(rssThumbnail, d), Duration: -1, ViewCount: -1, Uploaded: -1,
			UploadDate: strings.TrimSpace(first(rssDate, d)), Premium: strings.TrimSpace(it.Premium) == "true"}
		if l := strings.TrimSpace(first(rssLength, d)); l != "" {
			v.Duration = clockSeconds(l)
		}
		if n, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(first(rssViews, d)), ",", ""), 10, 64); err == nil {
			v.ViewCount = n
		}
		if t, err := time.ParseInLocation("2006年01月02日 15：04：05", v.UploadDate, time.FixedZone("UTC+9", 9*3600)); err == nil {
			v.Uploaded = t.UnixMilli()
		}
		videos = append(videos, v)
	}
	return info, videos, nil
}

// clockSeconds is utils.getDurationFromString: "h:m:s", "m:s" or "s".
func clockSeconds(s string) int64 {
	parts := strings.Split(s, ":")
	var total int64
	for i, unit := range []int64{1, 60, 3600} {
		if i >= len(parts) {
			break
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(parts[len(parts)-1-i]), 10, 64)
		total += n * unit
	}
	return total
}

func (c *Client) page(ctx context.Context, u string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("Accept-Language", "ja-JP")
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return "", fmt.Errorf("%w: channel HTTP %d", ErrUnavailable, res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return string(b), err
}
