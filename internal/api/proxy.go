package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"typetype.dev/server/internal/httpx"
)

const (
	browserUA       = "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Mobile Safari/537.36"
	bilibiliUA      = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	imageTTL        = 24 * time.Hour
	maxKeptImage    = 512 << 10
	shortLivedSecs  = 100
	keptCacheHeader = "public, max-age=86400"
)

var twitchChatImageHosts = map[string]bool{"cdn.betterttv.net": true, "cdn.7tv.app": true, "cdn.frankerfacez.com": true}

func hostIs(host, suffix string) bool { return host == suffix || strings.HasSuffix(host, "."+suffix) }

// proxyProvider is providerForProxyHost: only the CDNs of the supported services go through the proxy.
func proxyProvider(rawHost string) string {
	h := strings.TrimRight(strings.ToLower(rawHost), ".")
	switch {
	case hostIs(h, "googlevideo.com"), hostIs(h, "ytimg.com"), hostIs(h, "ggpht.com"), h == "yt3.googleusercontent.com":
		return "youtube"
	case hostIs(h, "bilivideo.com"), hostIs(h, "bilivideo.cn"), hostIs(h, "hdslb.com"), hostIs(h, "live.bilibili.com"),
		h == "upos-hz-mirrorakam.akamaized.net":
		return "bilibili"
	case hostIs(h, "nicovideo.jp"), hostIs(h, "nimg.jp"):
		return "niconico"
	case h == "static-cdn.jtvnw.net", twitchChatImageHosts[h]:
		return "twitch"
	}
	return ""
}

func validateProxyURL(raw string) (*url.URL, string) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "Malformed URL"
	}
	if u.Scheme == "" {
		return nil, "Missing URL scheme"
	}
	if strings.ToLower(u.Scheme) != "https" {
		return nil, "Unsupported URL scheme: " + strings.ToLower(u.Scheme)
	}
	if u.User != nil {
		return nil, "URL credentials are not allowed"
	}
	if u.Hostname() == "" {
		return nil, "Missing URL host"
	}
	if p := u.Port(); p != "" && p != "443" {
		return nil, "Unsupported proxy port"
	}
	if proxyProvider(u.Hostname()) == "" {
		return nil, "Unsupported proxy host"
	}
	return u, ""
}

// publicOnly refuses any connection to a private, loopback, link-local or multicast address, whatever DNS says.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return errors.New("proxy target resolves to a non-public address")
	}
	if ip4 := ip.To4(); ip4 != nil && (ip4[0] == 100 && ip4[1]&0xc0 == 64 || ip4[0] == 0 || ip4[0] >= 240) {
		return errors.New("proxy target resolves to a non-public address")
	}
	return nil
}

func newProxyClient() *http.Client {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: publicOnly}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if _, msg := validateProxyURL(req.URL.String()); msg != "" {
			return errors.New(msg)
		}
		return nil
	}}
}

var trackingParams = regexp.MustCompile(`[&?](cpn|pppid)=[^&]*`)
var googlevideoURL = regexp.MustCompile(`https://[a-z0-9.\-]+\.googlevideo\.com/\S+`)

type keptImage struct {
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

var copyBufs = sync.Pool{New: func() any { b := make([]byte, 64<<10); return &b }}

func (s *Server) proxyRoutes(rt *Router) {
	rt.Handle("GET", "/proxy", s.zones.proxy.Limit(nil, s.proxy))
}

func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	fetch, _, _ := strings.Cut(raw, "#")
	target, msg := validateProxyURL(fetch)
	if msg != "" {
		httpx.Error(w, r, http.StatusBadRequest, msg, "error")
		return
	}
	clean := trackingParams.ReplaceAllString(target.String(), "")
	rangeHeader := r.Header.Get("Range")
	provider := proxyProvider(target.Hostname())
	isImageHost := hostIs(target.Hostname(), "ytimg.com") || hostIs(target.Hostname(), "ggpht.com") || hostIs(target.Hostname(), "googleusercontent.com")
	key := ""
	if rangeHeader == "" && isImageHost {
		sum := sha256.Sum256([]byte(raw))
		key = "go:proxy-image:" + hex.EncodeToString(sum[:16])
		if v, ok, _ := s.Cache.Get(r.Context(), key); ok {
			var img keptImage
			if json.Unmarshal([]byte(v), &img) == nil {
				if body, err := base64.StdEncoding.DecodeString(img.Body); err == nil {
					writeKept(w, img.ContentType, body)
					return
				}
			}
		}
	}
	ctx := r.Context()
	if rangeHeader == "" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", clean, nil)
	if provider == "bilibili" {
		req.Header.Set("User-Agent", bilibiliUA)
		req.Header.Set("Referer", "https://www.bilibili.com")
		req.Header.Set("Accept", "*/*")
	} else {
		req.Header.Set("User-Agent", browserUA)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	res, err := s.proxyClient.Do(req)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Proxy fetch failed", "error")
		return
	}
	defer res.Body.Close()
	ct := res.Header.Get("Content-Type")
	thumbFallback := res.StatusCode == http.StatusNotFound && hostIs(target.Hostname(), "ytimg.com") && strings.HasPrefix(ct, "image/")
	if res.StatusCode/100 != 2 && !thumbFallback {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Upstream returned "+strconv.Itoa(res.StatusCode), "error")
		return
	}
	status := res.StatusCode
	if thumbFallback {
		status = http.StatusOK
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := w.Header()
	if strings.Contains(strings.ToLower(ct), "mpegurl") || strings.HasSuffix(strings.ToLower(target.Path), ".m3u8") {
		body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
		if err != nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Proxy fetch failed", "error")
			return
		}
		out := googlevideoURL.ReplaceAllStringFunc(string(body), func(m string) string { return "/proxy?url=" + url.QueryEscape(m) })
		h.Set("Content-Type", ct)
		if cc := res.Header.Get("Cache-Control"); cc != "" {
			h.Set("Cache-Control", cc)
		}
		w.WriteHeader(status)
		io.WriteString(w, out)
		return
	}
	if key != "" && status == http.StatusOK && strings.HasPrefix(ct, "image/") && maxAge(res.Header.Get("Cache-Control")) >= shortLivedSecs {
		head, err := io.ReadAll(io.LimitReader(res.Body, maxKeptImage+1))
		if err == nil && len(head) <= maxKeptImage {
			if b, err := json.Marshal(keptImage{ContentType: ct, Body: base64.StdEncoding.EncodeToString(head)}); err == nil {
				go s.Cache.Set(context.Background(), key, string(b), imageTTL)
			}
			writeKept(w, ct, head)
			return
		}
		h.Set("Content-Type", ct)
		w.WriteHeader(status)
		w.Write(head)
		copyBody(w, res.Body)
		return
	}
	h.Set("Content-Type", ct)
	for _, k := range []string{"Content-Range", "Accept-Ranges", "Cache-Control", "Content-Length"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	w.WriteHeader(status)
	copyBody(w, res.Body)
}

func copyBody(w io.Writer, r io.Reader) {
	buf := copyBufs.Get().(*[]byte)
	io.CopyBuffer(w, r, *buf)
	copyBufs.Put(buf)
}

func writeKept(w http.ResponseWriter, ct string, body []byte) {
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", keptCacheHeader)
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

var maxAgeRe = regexp.MustCompile(`max-age=(\d+)`)

func maxAge(cc string) int64 {
	m := maxAgeRe.FindStringSubmatch(cc)
	if m == nil {
		return -1
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	return n
}
