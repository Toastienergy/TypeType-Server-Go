package api

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	biliCheckInterval = 24 * time.Hour
	biliLeaseDuration = 30 * time.Minute
	biliCookieTTL     = 30 * 24 * time.Hour
)

// biliRotate is BiliBiliCookieRefreshClient.inspectAndRotate: when cookie/info asks for it, the correspond page
// gives a refresh csrf, cookie/refresh hands out new cookies and a new refresh token.
func biliRotate(ctx context.Context, cookies, refreshToken string) (biliRefresh, string, string, int64) {
	info, err := biliRefreshCall(ctx, "GET", biliPassport+"/cookie/info", cookies, nil)
	if err != nil || !info.ok || info.body == nil {
		return refreshRetry, "", "", 0
	}
	switch code, _ := jsonInt(info.body["code"]); code {
	case -101:
		return refreshReconnect, "", "", 0
	case 0:
	default:
		return refreshRetry, "", "", 0
	}
	data, _ := info.body["data"].(map[string]any)
	should, ok := jsonBool(data["refresh"])
	if !ok {
		return refreshRetry, "", "", 0
	}
	if !should {
		return refreshNotRequired, "", "", 0
	}
	ts, ok := jsonInt(data["timestamp"])
	if !ok || ts <= 0 {
		return refreshRetry, "", "", 0
	}
	path, err := correspondPath(ts)
	if err != nil {
		return refreshRetry, "", "", 0
	}
	page, err := biliRefreshPage(ctx, biliCorrespond+"/"+path, cookies)
	csrf := refreshCsrf(page)
	if err != nil || csrf == "" {
		return refreshRetry, "", "", 0
	}
	jct := biliCookieMap(map[string]string{}, strings.Split(cookies, ";"))["bili_jct"]
	if jct == "" {
		return refreshReconnect, "", "", 0
	}
	form := url.Values{"csrf": {jct}, "refresh_csrf": {csrf}, "source": {"main_web"}, "refresh_token": {refreshToken}}
	res, err := biliRefreshCall(ctx, "POST", biliPassport+"/cookie/refresh", cookies, form)
	if err != nil || !res.ok || res.body == nil {
		return refreshRetry, "", "", 0
	}
	switch code, _ := jsonInt(res.body["code"]); code {
	case -101, -111, 86095:
		return refreshReconnect, "", "", 0
	case 0:
	default:
		return refreshRetry, "", "", 0
	}
	rdata, _ := res.body["data"].(map[string]any)
	token, _ := rdata["refresh_token"].(string)
	if token = strings.TrimSpace(token); token == "" || len(token) > 512 {
		return refreshRetry, "", "", 0
	}
	merged := biliCookieMap(biliCookieMap(map[string]string{}, strings.Split(cookies, ";")), res.setCookies)
	if merged["SESSDATA"] == "" || merged["bili_jct"] == "" {
		return refreshReconnect, "", "", 0
	}
	expires := time.Now().Add(biliCookieTTL).UnixMilli()
	if f := strings.Split(merged["SESSDATA"], ","); len(f) > 1 {
		if n, err := strconv.ParseInt(f[1], 10, 64); err == nil && n >= 1_000_000_000 {
			expires = n * 1000
		}
	}
	return refreshRotated, biliCookieHeader(merged), token, expires
}

func biliRefreshPage(ctx context.Context, u, cookies string) (string, error) {
	c, err := biliRefreshCall(ctx, "GET", u, cookies, nil)
	if err != nil {
		return "", err
	}
	return c.page, nil
}

// biliConfirm is confirmRotation: the old refresh token is retired; a refused confirmation is checked on cookie/info.
func biliConfirm(ctx context.Context, cookies, oldToken string) biliRefresh {
	jct := biliCookieMap(map[string]string{}, strings.Split(cookies, ";"))["bili_jct"]
	if jct == "" {
		return refreshReconnect
	}
	res, err := biliRefreshCall(ctx, "POST", biliPassport+"/confirm/refresh", cookies, url.Values{"csrf": {jct}, "refresh_token": {oldToken}})
	if err != nil || !res.ok || res.body == nil {
		return refreshRetry
	}
	switch code, _ := jsonInt(res.body["code"]); code {
	case 0:
		return refreshConfirmed
	case -101, -111:
		info, err := biliRefreshCall(ctx, "GET", biliPassport+"/cookie/info", cookies, nil)
		if err != nil || !info.ok || info.body == nil {
			return refreshRetry
		}
		switch code, _ := jsonInt(info.body["code"]); code {
		case -101:
			return refreshReconnect
		case 0:
			data, _ := info.body["data"].(map[string]any)
			if should, ok := jsonBool(data["refresh"]); ok && !should {
				return refreshConfirmed
			}
		}
	}
	return refreshRetry
}

type biliLease struct {
	user, cookies, token, pending string
	hasPending                    bool
	until                         int64
}

// startBiliRefresh is BiliBiliSessionRefreshScheduler: a minute after start, then hourly, up to twenty connected
// sessions whose check is a day old or whose rotation awaits confirmation are leased for thirty minutes and refreshed.
func (s *Server) startBiliRefresh() {
	if s.biliCrypto() == nil {
		return
	}
	go func() {
		time.Sleep(time.Minute)
		for {
			s.refreshBiliSessions()
			time.Sleep(time.Hour)
		}
	}()
}

func (s *Server) refreshBiliSessions() int {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	leases := s.claimBiliSessions(ctx, time.Now().UnixMilli())
	for _, l := range leases {
		s.refreshBiliSession(ctx, l)
	}
	return len(leases)
}

func (s *Server) claimBiliSessions(ctx context.Context, now int64) []biliLease {
	rows, err := s.DB.Query(ctx, `SELECT user_id, encrypted_cookies, encrypted_refresh_token, COALESCE(encrypted_pending_confirm_token, ''),
encrypted_pending_confirm_token IS NOT NULL, refresh_claimed_until FROM bilibili_sessions WHERE status = 'connected'
AND encrypted_refresh_token IS NOT NULL AND (encrypted_pending_confirm_token IS NOT NULL OR refresh_checked_at <= $1)
ORDER BY refresh_checked_at ASC LIMIT 1000`, now-biliCheckInterval.Milliseconds())
	if err != nil {
		slog.Warn("bilibili session refresh cycle failed", "err", err)
		return nil
	}
	var found []biliLease
	for rows.Next() {
		var l biliLease
		if rows.Scan(&l.user, &l.cookies, &l.token, &l.pending, &l.hasPending, &l.until) == nil {
			found = append(found, l)
		}
	}
	rows.Close()
	var out []biliLease
	for _, l := range found {
		if len(out) >= 20 || l.until > now {
			continue
		}
		until := now + biliLeaseDuration.Milliseconds()
		tag, err := s.DB.Exec(ctx, `UPDATE bilibili_sessions SET refresh_claimed_until = $4 WHERE user_id = $1 AND status = 'connected'
AND refresh_claimed_until = $2 AND encrypted_refresh_token = $3`, l.user, l.until, l.token, until)
		if err == nil && tag.RowsAffected() > 0 {
			l.until = until
			out = append(out, l)
		}
	}
	return out
}
