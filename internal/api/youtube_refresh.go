package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// youtubeCookieRefresh is YoutubeSessionCookieRefreshClient: the signed-in subscriptions page refreshes the session
// cookies YouTube rotates; a redirect to Google sign-in or a 401/403 means the session is gone.
type youtubeRefresh int

const (
	ytRefreshRetry youtubeRefresh = iota
	ytRefreshNotRequired
	ytRefreshReconnect
	ytRefreshed
)

var (
	youtubeSubscriptionsURL = "https://www.youtube.com/feed/subscriptions"
	youtubeRefreshWeb       = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
)

func refreshYoutubeCookies(ctx context.Context, cookies string) (youtubeRefresh, string) {
	req, _ := http.NewRequestWithContext(ctx, "GET", youtubeSubscriptionsURL, nil)
	req.Header.Set("Cookie", cookies)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	res, err := youtubeRefreshWeb.Do(req)
	if err != nil {
		return ytRefreshRetry, ""
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
	switch {
	case strings.Contains(res.Header.Get("Location"), "accounts.google.com"), res.StatusCode == 401, res.StatusCode == 403:
		return ytRefreshReconnect, ""
	case res.StatusCode/100 != 2:
		return ytRefreshRetry, ""
	}
	merged := mergeYoutubeCookies(cookies, res.Header.Values("Set-Cookie"))
	if merged == cookies {
		return ytRefreshNotRequired, ""
	}
	return ytRefreshed, merged
}

// mergeYoutubeCookies is YoutubeSessionCookieJar.merge: the allowed cookies a response sets replace or remove
// those of the header, in their first order.
func mergeYoutubeCookies(header string, setCookies []string) string {
	var order []string
	values := map[string]string{}
	put := func(name, value string) {
		if _, ok := values[name]; !ok {
			order = append(order, name)
		}
		values[name] = value
	}
	for _, part := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(part, "=")
		if name, value = strings.TrimSpace(name), strings.TrimSpace(value); ok && name != "" && value != "" {
			put(name, value)
		}
	}
	for _, h := range setCookies {
		pair, _, _ := strings.Cut(h, ";")
		name, value, ok := strings.Cut(pair, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" || !allowedYoutubeCookies[name] {
			continue
		}
		if value == "" {
			delete(values, name)
			continue
		}
		put(name, value)
	}
	var out []string
	for _, n := range order {
		if v, ok := values[n]; ok {
			out = append(out, n+"="+v)
		}
	}
	return strings.Join(out, "; ")
}

// startYoutubeRefresh is YoutubeSessionRefreshScheduler: ten minutes after start, then every six hours.
func (s *Server) startYoutubeRefresh() {
	if s.youtubeCrypto() == nil {
		return
	}
	go func() {
		time.Sleep(10 * time.Minute)
		for {
			s.refreshYoutubeSessions()
			time.Sleep(6 * time.Hour)
		}
	}()
}

func (s *Server) refreshYoutubeSessions() int {
	c := s.youtubeCrypto()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	rows, err := s.DB.Query(ctx, "SELECT user_id, encrypted_cookies FROM youtube_sessions WHERE status = 'connected' AND auth_type = 'cookies'")
	if err != nil {
		s.Log.Warn("youtube session refresh cycle failed", "err", err)
		return 0
	}
	type row struct{ user, enc string }
	var all []row
	for rows.Next() {
		var x row
		if rows.Scan(&x.user, &x.enc) == nil {
			all = append(all, x)
		}
	}
	rows.Close()
	refreshed := 0
	for _, x := range all {
		cookies, err := c.decrypt(x.enc)
		if err != nil {
			continue
		}
		switch res, merged := refreshYoutubeCookies(ctx, cookies); res {
		case ytRefreshed:
			s.DB.Exec(ctx, "UPDATE youtube_sessions SET encrypted_cookies = $2, updated_at = $3 WHERE user_id = $1", x.user, c.encrypt(merged), time.Now().UnixMilli())
			refreshed++
		case ytRefreshReconnect:
			s.youtubeNeedsReconnect(ctx, x.user)
		}
	}
	return refreshed
}
