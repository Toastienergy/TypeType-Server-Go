// Package bilibili talks to Bilibili's web API the way PipePipe's BilibiliService does: a forged desktop Chrome,
// anonymous visitor cookies with a web ticket, and WBI-signed queries.
package bilibili

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	wwwReferer   = "https://www.bilibili.com/"
	spaceReferer = "https://space.bilibili.com/"
	liveReferer  = "https://live.bilibili.com/"
)

// ErrRiskControl is Bilibili's WAF (HTTP 412) or a -352/-412 code: the server's address is being challenged.
var ErrRiskControl = errors.New("bilibili risk control")

type device struct {
	ua, webgl, renderer string
	width, height       int
}

var cards = [][2]string{{"AMD", "AMD Radeon RX 6600 (0x000073FF)"}, {"AMD", "AMD Radeon(TM) Graphics (0x00001638)"},
	{"Intel", "Intel(R) UHD Graphics 620 (0x00005917)"}, {"Intel", "Intel(R) UHD Graphics 630 (0x00003E92)"},
	{"Intel", "Intel(R) Iris(R) Xe Graphics (0x000046A8)"}, {"NVIDIA", "NVIDIA GeForce GTX 1060 6GB (0x00001C03)"},
	{"NVIDIA", "NVIDIA GeForce RTX 3060 (0x00002504)"}, {"NVIDIA", "NVIDIA GeForce RTX 4070 (0x00002786)"}}

func forgeDevice() device {
	c := cards[mrand.IntN(len(cards))]
	return device{ua: fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", 130+mrand.IntN(8)),
		webgl: "WebGL 1.0 (OpenGL ES 2.0 Chromium)", renderer: fmt.Sprintf("ANGLE (%s, %s Direct3D11 vs_5_0 ps_5_0, D3D11)Google Inc. (%s)", c[0], c[1], c[0]),
		width: 1920 - 60 - mrand.IntN(60), height: 1080 - 90 - mrand.IntN(60)}
}

// b64Cut is utils.encodeToBase64SubString: base64 without its last two characters.
func b64Cut(s string) string {
	e := base64.StdEncoding.EncodeToString([]byte(s))
	return e[:len(e)-2]
}

type Client struct {
	http          *http.Client
	dev           device
	mu            sync.Mutex
	cookies       string
	cookieExpires int64
	mixin         string
	mixinDay      string
}

func NewClient(h *http.Client) *Client {
	if h == nil {
		h = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{http: h, dev: forgeDevice()}
}

func (c *Client) UserAgent() string { return c.dev.ua }

// Reset forges a new device and drops the visitor cookies, as PipePipe does after a risk-control answer.
func (c *Client) Reset() {
	c.mu.Lock()
	c.dev, c.cookies, c.cookieExpires = forgeDevice(), "", 0
	c.mu.Unlock()
}

func referer(orig string) string {
	if u, err := url.Parse(orig); err == nil && u.Host != "" {
		if r := "https://" + u.Host + "/"; r == wwwReferer || r == spaceReferer || r == liveReferer {
			return r
		}
	}
	return wwwReferer
}

func (c *Client) uaHeaders(req *http.Request, orig string) {
	req.Header.Set("User-Agent", c.dev.ua)
	req.Header.Set("Referer", referer(orig))
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
}

func fpUUID() string {
	digits := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "A", "B", "C", "D", "E", "F", "10"}
	b := make([]byte, 32)
	rand.Read(b)
	var sb strings.Builder
	for i, x := range b {
		if i == 9 || i == 13 || i == 17 || i == 21 {
			sb.WriteByte('-')
		}
		sb.WriteString(digits[x&0x0f])
	}
	fmt.Fprintf(&sb, "%05dinfoc", time.Now().UnixMilli()%100000)
	return sb.String()
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// visitorCookies are getDefaultCookies: buvid3/buvid4 from the fingerprint API, a web ticket, and random ids, kept
// until the ticket expires.
func (c *Client) visitorCookies(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.cookies != "" && c.cookieExpires > time.Now().Unix() {
		v := c.cookies
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.bilibili.com/x/frontend/finger/spi", nil)
	c.uaHeaders(req, wwwReferer)
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	var spi struct {
		Data struct {
			B3 string `json:"b_3"`
			B4 string `json:"b_4"`
		} `json:"data"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&spi)
	res.Body.Close()
	if err != nil || spi.Data.B3 == "" {
		return "", fmt.Errorf("bilibili spi: %v", err)
	}
	nut := time.Now().Unix()
	if d, err := http.ParseTime(res.Header.Get("Date")); err == nil {
		nut = d.Unix()
	}
	parts := []string{"buvid3=" + spi.Data.B3, "b_nut=" + strconv.FormatInt(nut, 10),
		"b_lsid=" + strings.ToUpper(randHex(32)) + "_" + strings.ToUpper(strconv.FormatInt(time.Now().UnixMilli(), 16)),
		"_uuid=" + fpUUID(), "buvid4=" + spi.Data.B4}
	ticket, expires, err := c.webTicket(ctx, strings.Join(parts, "; "))
	if err != nil {
		return "", err
	}
	parts = append(parts, "bili_ticket="+ticket, "bili_ticket_expires="+strconv.FormatInt(expires, 10), "buvid_fp="+randHex(16))
	cookie := strings.Join(parts, "; ")
	c.mu.Lock()
	c.cookies, c.cookieExpires = cookie, expires
	c.mu.Unlock()
	return cookie, nil
}

func (c *Client) webTicket(ctx context.Context, cookies string) (string, int64, error) {
	ts := time.Now().Unix()
	m := hmac.New(sha256.New, []byte("XgwSnGZ1p"))
	m.Write([]byte("ts" + strconv.FormatInt(ts, 10)))
	u := "https://api.bilibili.com/bapis/bilibili.api.ticket.v1.Ticket/GenWebTicket?key_id=ec02&hexsign=" + hex.EncodeToString(m.Sum(nil)) +
		"&context%5Bts%5D=" + strconv.FormatInt(ts, 10) + "&csrf="
	req, _ := http.NewRequestWithContext(ctx, "POST", u, nil)
	c.uaHeaders(req, wwwReferer)
	req.Header.Set("Cookie", cookies)
	res, err := c.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer res.Body.Close()
	var t struct {
		Data struct {
			Ticket    string `json:"ticket"`
			CreatedAt int64  `json:"created_at"`
			TTL       int64  `json:"ttl"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&t); err != nil || t.Data.Ticket == "" || t.Data.CreatedAt <= 0 || t.Data.TTL <= 0 {
		return "", 0, fmt.Errorf("bilibili ticket: %v", err)
	}
	return t.Data.Ticket, t.Data.CreatedAt + t.Data.TTL, nil
}

// Get fetches a Bilibili API URL with the visitor cookies and decodes its JSON envelope.
func (c *Client) Get(ctx context.Context, u, orig string) (map[string]any, error) {
	cookies := accountCookies(ctx, u)
	if cookies == "" {
		var err error
		if cookies, err = c.visitorCookies(ctx); err != nil {
			return nil, err
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	c.uaHeaders(req, orig)
	req.Header.Set("Cookie", cookies)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusPreconditionFailed {
		return nil, ErrRiskControl
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("bilibili: HTTP %d", res.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

var mixinTable = []int{46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41,
	13, 37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52}

var shanghai = func() *time.Location {
	if l, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return l
	}
	return time.FixedZone("CST", 8*3600)
}()

func keyOf(u string) string {
	base := u[strings.LastIndex(u, "/")+1:]
	k, _, _ := strings.Cut(base, ".")
	return k
}

// mixinKey is refreshed once a day in China's time zone, as the web player does.
func (c *Client) mixinKey(ctx context.Context) (string, error) {
	day := time.Now().In(shanghai).Format("2006-01-02")
	c.mu.Lock()
	if c.mixin != "" && c.mixinDay == day {
		k := c.mixin
		c.mu.Unlock()
		return k, nil
	}
	c.mu.Unlock()
	nav, err := c.Get(ctx, "https://api.bilibili.com/x/web-interface/nav", wwwReferer)
	if err != nil && !errors.Is(err, ErrRiskControl) {
		return "", err
	}
	data, _ := nav["data"].(map[string]any)
	img, _ := data["wbi_img"].(map[string]any)
	iu, _ := img["img_url"].(string)
	su, _ := img["sub_url"].(string)
	if iu == "" || su == "" {
		return "", errors.New("bilibili: no wbi keys")
	}
	ae := keyOf(iu) + keyOf(su)
	var b strings.Builder
	for _, i := range mixinTable {
		if i < len(ae) {
			b.WriteByte(ae[i])
		}
	}
	k := b.String()
	if len(k) > 32 {
		k = k[:32]
	}
	c.mu.Lock()
	c.mixin, c.mixinDay = k, day
	c.mu.Unlock()
	return k, nil
}

// javaEncode is URLEncoder.encode with spaces as %20, which the signature is computed over.
func javaEncode(s string) string {
	e := url.QueryEscape(s)
	return strings.NewReplacer("+", "%20", "~", "%7E", "%2A", "*").Replace(e)
}

// Signed returns base?params with wts and w_rid added (encWbi).
func (c *Client) Signed(ctx context.Context, base string, params map[string]string) (string, error) {
	key, err := c.mixinKey(ctx)
	if err != nil {
		return "", err
	}
	p := map[string]string{}
	for k, v := range params {
		p[k] = v
	}
	p["wts"] = strconv.FormatInt(time.Now().Unix(), 10)
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = javaEncode(k) + "=" + javaEncode(p[k])
	}
	q := strings.Join(parts, "&")
	sum := md5.Sum([]byte(q + key))
	return base + "?" + q + "&w_rid=" + hex.EncodeToString(sum[:]), nil
}

// DmImgParams are the fake canvas and pointer fields the web player sends with signed calls.
func (c *Client) DmImgParams() map[string]string {
	r1, r2 := mrand.IntN(114), mrand.IntN(514)
	w, h := c.dev.width, c.dev.height
	return map[string]string{"dm_img_list": "[]", "dm_img_str": b64Cut(c.dev.webgl), "dm_cover_img_str": b64Cut(c.dev.renderer),
		"dm_img_inter": fmt.Sprintf(`{"ds":[],"wh":[%d,%d,%d],"of":[%d,%d,%d]}`, 2*w+2*h+3*r1, 4*w-h+r1, r1, r2, 2*r2, r2)}
}
