package bilibili

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const bvTable = "FcwAPNKTMug3GV5Lj7EJnHpWsx4tb8haYeviqBz6rkCy12mUSDQX9RdoZf"

var (
	xorCode  = big.NewInt(23442827791579)
	maskCode = big.NewInt(2251799813685247)
	maxAid   = new(big.Int).Lsh(big.NewInt(1), 51)
	base58   = big.NewInt(58)
	bvRe     = regexp.MustCompile(`BV[0-9A-Za-z]{10}`)
	avRe     = regexp.MustCompile(`(?i)/video/av([0-9]+)`)
)

func BV2AV(bv string) int64 {
	b := []byte(bv)
	if len(b) < 12 {
		return 0
	}
	b[3], b[9] = b[9], b[3]
	b[4], b[7] = b[7], b[4]
	t := new(big.Int)
	for _, ch := range b[3:] {
		t.Mul(t, base58).Add(t, big.NewInt(int64(strings.IndexByte(bvTable, ch))))
	}
	return t.And(t, maskCode).Xor(t, xorCode).Int64()
}

func AV2BV(aid int64) string {
	b := []byte("BV1000000000")
	t := new(big.Int).Or(maxAid, big.NewInt(aid))
	t.Xor(t, xorCode)
	i := len(b) - 1
	m := new(big.Int)
	for t.Sign() > 0 {
		t.DivMod(t, base58, m)
		b[i] = bvTable[m.Int64()]
		i--
	}
	b[3], b[9] = b[9], b[3]
	b[4], b[7] = b[7], b[4]
	return string(b)
}

// BVID reads the video id out of a Bilibili video URL, turning an av id into its BV form.
func BVID(raw string) string {
	if m := bvRe.FindString(raw); m != "" {
		return m
	}
	if m := avRe.FindStringSubmatch(raw); m != nil {
		if aid, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return AV2BV(aid)
		}
	}
	return ""
}

// Media is one DASH representation; URL is the stablest mirror and Alternates the others to fall back on.
type Media struct {
	URL                                      string
	Alternates                               []string
	Codec                                    string
	Bandwidth                                int64
	Width, Height, Fps                       int
	QualityID                                int
	InitStart, InitEnd, IndexStart, IndexEnd int64
}

type Video struct {
	BVID, Title, Description, Thumbnail string
	OwnerName, OwnerFace                string
	OwnerMid                            int64
	Views, Likes                        int64
	Duration, PubDate                   int64
	CID                                 int64
	Tags                                []string
	VideoOnly                           []Media
	Audio                               []Media
}

var ErrUnavailable = errors.New("bilibili video unavailable")

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func obj(v any, keys ...string) map[string]any {
	m, _ := v.(map[string]any)
	for _, k := range keys {
		m, _ = m[k].(map[string]any)
	}
	return m
}

func arr(v any, key string) []any {
	m, _ := v.(map[string]any)
	a, _ := m[key].([]any)
	return a
}

func str(v any, key string) string {
	m, _ := v.(map[string]any)
	s, _ := m[key].(string)
	return s
}

func isMcdn(u string) bool {
	return strings.Contains(u, "mcdn.bilivideo") || strings.Contains(u, "mountaintoys") || strings.Contains(u, "os=mcdn")
}

func urlRank(u string) int {
	if isMcdn(u) {
		return 2
	}
	h := u
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/:?"); i >= 0 {
		h = h[:i]
	}
	if strings.HasPrefix(h, "upos-") && strings.Contains(h, "mirror") {
		return 0
	}
	return 1
}

// pickURLs is BilibiliMediaUrls.from: the best-ranked address, then up to two non-M-CDN alternates.
func pickURLs(primary string, backups []any) (string, []string) {
	cands := []string{primary}
	for _, b := range backups {
		if s, ok := b.(string); ok {
			cands = append(cands, s)
		}
	}
	best, rank := "", 99
	for _, c := range cands {
		if c != "" && urlRank(c) < rank {
			best, rank = c, urlRank(c)
		}
	}
	var alts []string
	seen := map[string]bool{}
	for _, c := range cands {
		if c != "" && c != best && !isMcdn(c) && !seen[c] && len(alts) < 2 {
			seen[c] = true
			alts = append(alts, c)
		}
	}
	return best, alts
}

func ranges(r string) (int64, int64) {
	a, b, _ := strings.Cut(r, "-")
	x, _ := strconv.ParseInt(a, 10, 64)
	y, _ := strconv.ParseInt(b, 10, 64)
	return x, y
}

func parseFps(s string) int {
	if a, b, ok := strings.Cut(s, "/"); ok {
		x, _ := strconv.ParseFloat(a, 64)
		y, _ := strconv.ParseFloat(b, 64)
		if y > 0 {
			return int(x / y)
		}
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int(f)
}

func media(o map[string]any) Media {
	baseURL := str(o, "baseUrl")
	if baseURL == "" {
		baseURL = str(o, "base_url")
	}
	backups := arr(o, "backupUrl")
	if len(backups) == 0 {
		backups = arr(o, "backup_url")
	}
	m := Media{Codec: str(o, "codecs"), Bandwidth: num(o["bandwidth"]), Width: int(num(o["width"])), Height: int(num(o["height"])),
		Fps: parseFps(str(o, "frameRate")), QualityID: int(num(o["id"]))}
	m.URL, m.Alternates = pickURLs(baseURL, backups)
	sb := obj(o, "SegmentBase")
	if sb == nil {
		sb = obj(o, "segment_base")
	}
	init := str(sb, "Initialization")
	if init == "" {
		init = str(sb, "initialization")
	}
	idx := str(sb, "indexRange")
	if idx == "" {
		idx = str(sb, "index_range")
	}
	m.InitStart, m.InitEnd = ranges(init)
	m.IndexStart, m.IndexEnd = ranges(idx)
	return m
}

func codeErr(res map[string]any, what string) error {
	switch c := num(res["code"]); c {
	case 0:
		return nil
	case -352, -412:
		return ErrRiskControl
	default:
		msg, _ := res["message"].(string)
		return fmt.Errorf("%w: %s %d %s", ErrUnavailable, what, c, msg)
	}
}

// Video loads a video's metadata and its DASH streams, like BillibiliStreamExtractor for an ordinary video.
func (c *Client) Video(ctx context.Context, rawURL string) (*Video, error) {
	bv := BVID(rawURL)
	if bv == "" {
		return nil, fmt.Errorf("%w: no BV id", ErrUnavailable)
	}
	view, err := c.Get(ctx, "https://api.bilibili.com/x/web-interface/wbi/view?bvid="+bv, rawURL)
	if err != nil {
		return nil, err
	}
	if err := codeErr(view, "view"); err != nil {
		return nil, err
	}
	d := obj(view, "data")
	v := &Video{BVID: bv, Title: str(d, "title"), Description: str(d, "desc"), Thumbnail: strings.Replace(str(d, "pic"), "http://", "https://", 1),
		OwnerName: str(obj(d, "owner"), "name"), OwnerFace: str(obj(d, "owner"), "face"), OwnerMid: num(obj(d, "owner")["mid"]),
		Views: num(obj(d, "stat")["view"]), Likes: num(obj(d, "stat")["like"]), PubDate: num(d["pubdate"])}
	pages := arr(d, "pages")
	p := 1
	if u, err := url.Parse(rawURL); err == nil && u.Query().Has("p") {
		if p, err = strconv.Atoi(u.Query().Get("p")); err != nil || p < 1 || p > len(pages) {
			return nil, fmt.Errorf("%w: page out of range", ErrUnavailable)
		}
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("%w: no pages", ErrUnavailable)
	}
	page, _ := pages[p-1].(map[string]any)
	v.CID, v.Duration = num(page["cid"]), num(page["duration"])
	if v.CID <= 0 {
		v.CID = num(d["cid"])
	}
	params := map[string]string{"avid": strconv.FormatInt(BV2AV(bv), 10), "bvid": bv, "cid": strconv.FormatInt(v.CID, 10), "qn": "120",
		"fnver": "0", "fnval": "4048", "fourk": "1", "try_look": "1", "web_location": "1315873"}
	for k, val := range c.DmImgParams() {
		params[k] = val
	}
	signed, err := c.Signed(ctx, "https://api.bilibili.com/x/player/wbi/playurl", params)
	if err != nil {
		return nil, err
	}
	play, err := c.Get(ctx, signed, rawURL)
	if err != nil {
		return nil, err
	}
	if err := codeErr(play, "playurl"); err != nil {
		return nil, err
	}
	pd := obj(play, "data")
	dash := obj(pd, "dash")
	if len(dash) == 0 {
		if str(pd, "v_voucher") != "" && len(arr(pd, "durl")) == 0 {
			return nil, ErrRiskControl
		}
		return nil, fmt.Errorf("%w: paid content", ErrUnavailable)
	}
	for _, x := range arr(dash, "video") {
		if o, ok := x.(map[string]any); ok {
			v.VideoOnly = append(v.VideoOnly, media(o))
		}
	}
	audio := arr(dash, "audio")
	audio = append(arr(obj(dash, "dolby"), "audio"), audio...)
	if f := obj(dash, "flac", "audio"); len(f) > 0 {
		audio = append([]any{f}, audio...)
	}
	for _, x := range audio {
		if o, ok := x.(map[string]any); ok {
			v.Audio = append(v.Audio, media(o))
		}
	}
	return v, nil
}

// Related is the "related videos" list of the watch page.
func (c *Client) Related(ctx context.Context, bv string) ([]map[string]any, error) {
	res, err := c.Get(ctx, "https://api.bilibili.com/x/web-interface/archive/related?bvid="+bv, wwwReferer)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, x := range arr(res, "data") {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// Trending is the home page's recommended feed, which the Kotlin server serves as Bilibili trending.
func (c *Client) Trending(ctx context.Context) ([]map[string]any, error) {
	res, err := c.Get(ctx, "https://api.bilibili.com/x/web-interface/index/top/rcmd?fresh_type=3", wwwReferer)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, x := range arr(obj(res, "data"), "item") {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out, nil
}
