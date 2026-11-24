package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// biliRefresh is the outcome of BiliBiliCookieRefreshClient: rotation results and confirmation results share it.
type biliRefresh int

const (
	refreshRetry biliRefresh = iota
	refreshNotRequired
	refreshReconnect
	refreshRotated
	refreshConfirmed
)

var (
	biliPassport   = "https://passport.bilibili.com/x/passport-login/web"
	biliCorrespond = "https://www.bilibili.com/correspond/1"
	biliRefreshWeb = &http.Client{Timeout: 20 * time.Second}
)

const biliCorrespondKey = "MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDLgd2OAkcGVtoE3ThUREbio0EgUc/prcajMKXvkCKFCWhJYJcLkcM2DKKcSeFpD/j6Boy538YXnR6VhcuUJOhH2x71" +
	"nzPjfdTcqMz7djHum0qSZA0AyCBDABUqCrfNgCiJ00Ra7GmRj+YCK1NJEuewlb40JNrRuoEUXpabUzGB8QIDAQAB"

// correspondPath is BiliBiliCookieRefreshCrypto.correspondPath: "refresh_<ms>" under RSA-OAEP SHA-256, in hex.
func correspondPath(timestamp int64) (string, error) {
	der, _ := base64.StdEncoding.DecodeString(biliCorrespondKey)
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return "", err
	}
	out, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key.(*rsa.PublicKey), []byte("refresh_"+strconv.FormatInt(timestamp, 10)), nil)
	return hex.EncodeToString(out), err
}

var refreshCsrfPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// refreshCsrf is the text of the correspond page's div#1-name.
func refreshCsrf(page string) string {
	z := html.NewTokenizer(strings.NewReader(page))
	inside := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken:
			t := z.Token()
			inside = t.Data == "div" && tokenAttr(t, "id") == "1-name"
		case html.EndTagToken:
			if z.Token().Data == "div" {
				inside = false
			}
		case html.TextToken:
			if v := strings.TrimSpace(string(z.Text())); inside && v != "" {
				if refreshCsrfPattern.MatchString(v) {
					return v
				}
				return ""
			}
		}
	}
}

func tokenAttr(t html.Token, key string) string {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

type biliCall struct {
	ok         bool
	setCookies []string
	page       string
	body       map[string]any
}

func biliRefreshCall(ctx context.Context, method, u, cookies string, form url.Values) (biliCall, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return biliCall{}, err
	}
	req.Header.Set("Cookie", cookies)
	req.Header.Set("User-Agent", biliWebUA)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := biliRefreshWeb.Do(req)
	if err != nil {
		return biliCall{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return biliCall{}, err
	}
	c := biliCall{ok: res.StatusCode/100 == 2, setCookies: res.Header.Values("Set-Cookie"), page: string(raw)}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.Decode(&c.body)
	return c, nil
}

func jsonInt(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		n, err := strconv.ParseInt(x.String(), 10, 64)
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	}
	return 0, false
}

func jsonBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		return x == "true", x == "true" || x == "false"
	}
	return false, false
}

// biliCookieMap keeps the login cookies of "a=b; c=d" pairs or of Set-Cookie lines.
func biliCookieMap(m map[string]string, parts []string) map[string]string {
	for _, p := range parts {
		name, value, ok := strings.Cut(strings.TrimSpace(strings.SplitN(p, ";", 2)[0]), "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if ok && value != "" && contains(biliLoginCookies, name) {
			m[name] = value
		}
	}
	return m
}

func biliCookieHeader(m map[string]string) string {
	var out []string
	for _, n := range biliLoginCookies {
		if v, ok := m[n]; ok {
			out = append(out, n+"="+v)
		}
	}
	return strings.Join(out, "; ")
}
