package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const webVersion = "2.20261120.01.00"

type ClientInfo struct {
	Name, Version, UA string
	ID                int
	Extra             map[string]any
}

var (
	Web = ClientInfo{Name: "WEB", Version: webVersion, ID: 1,
		UA: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"}
	AndroidVR = ClientInfo{Name: "ANDROID_VR", Version: "1.65.10", ID: 28,
		UA:    "com.google.android.apps.youtube.vr.oculus/1.65.10 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip",
		Extra: map[string]any{"deviceMake": "Oculus", "deviceModel": "Quest 3", "androidSdkVersion": 32, "osName": "Android", "osVersion": "12L"}}
)

// Client calls innertube with one warm connection pool and a visitor id shared by every call, refreshed hourly.
type Client struct {
	http *http.Client
	mu   sync.Mutex
	vis  string
	at   time.Time
}

func NewClient(proxy string) *Client {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if proxy != "" {
		if u, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &Client{http: &http.Client{Transport: tr, Timeout: 20 * time.Second}}
}

var visitorRe = regexp.MustCompile(`"VISITOR_DATA":"([^"]+)"`)

// Visitor returns a visitor id from the embed page, kept for an hour.
func (c *Client) Visitor(ctx context.Context) string {
	c.mu.Lock()
	if c.vis != "" && time.Since(c.at) < time.Hour {
		v := c.vis
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://www.youtube.com/embed/aqz-KE-bpKQ", nil)
	req.Header.Set("User-Agent", Web.UA)
	res, err := c.http.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	m := visitorRe.FindSubmatch(body)
	if m == nil {
		return ""
	}
	c.mu.Lock()
	c.vis, c.at = string(m[1]), time.Now()
	c.mu.Unlock()
	return string(m[1])
}

var ErrStatus = errors.New("innertube status")

// Call posts to /youtubei/v1/{endpoint} as the given client and decodes the answer.
func (c *Client) Call(ctx context.Context, ci ClientInfo, endpoint string, body map[string]any) (map[string]any, error) {
	visitor := c.Visitor(ctx)
	client := map[string]any{"clientName": ci.Name, "clientVersion": ci.Version, "hl": "en", "gl": "US"}
	if visitor != "" {
		client["visitorData"] = visitor
	}
	for k, v := range ci.Extra {
		client[k] = v
	}
	body["context"] = map[string]any{"client": client}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://www.youtube.com/youtubei/v1/"+endpoint+"?prettyPrint=false", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ci.UA)
	req.Header.Set("X-YouTube-Client-Name", strconv.Itoa(ci.ID))
	req.Header.Set("X-YouTube-Client-Version", ci.Version)
	if visitor != "" {
		req.Header.Set("X-Goog-Visitor-Id", visitor)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil, fmt.Errorf("%w %d on %s", ErrStatus, res.StatusCode, endpoint)
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
