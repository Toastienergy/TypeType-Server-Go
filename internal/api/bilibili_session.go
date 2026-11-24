package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/httpx"
)

const biliWebUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var biliLoginCookies = []string{"SESSDATA", "bili_jct", "buvid3", "DedeUserID", "DedeUserID__ckMd5", "sid"}

func (s *Server) biliCrypto() *sessionCrypto {
	return newSessionCrypto(secretConfig("BILIBILI_SESSION_ENCRYPTION_KEY"), "typetype.bilibili-session")
}

var biliNoRedirect = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func biliGet(ctx context.Context, client *http.Client, u, cookie string) (*http.Response, map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", biliWebUA)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		root = nil
	}
	return res, root, nil
}

func loginCookies(res *http.Response) map[string]string {
	out := map[string]string{}
	for _, h := range res.Header.Values("Set-Cookie") {
		pair := strings.SplitN(strings.SplitN(h, ";", 2)[0], "=", 2)
		if len(pair) == 2 && pair[1] != "" && inList(pair[0], biliLoginCookies) {
			out[pair[0]] = pair[1]
		}
	}
	return out
}

func inList(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// biliQRPoll is BiliBiliQrLoginService.poll: the account cookies once the QR is confirmed, from the poll answer or
// the passport.biligame.com cross-domain page it points at.
func biliQRPoll(ctx context.Context, key string) (status, message, cookies, refresh string) {
	res, root, err := biliGet(ctx, biliNoRedirect, "https://passport.bilibili.com/x/passport-login/web/qrcode/poll?qrcode_key="+url.QueryEscape(key), "")
	if err != nil {
		return "error", err.Error(), "", ""
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		return "error", "Missing data", "", ""
	}
	code, ok := data["code"].(float64)
	if !ok {
		return "error", "Missing code", "", ""
	}
	switch int(code) {
	case 0:
	case 86038:
		return "expired", "", "", ""
	case 86090:
		return "scanned", "", "", ""
	case 86001, 86101:
		return "waiting", "", "", ""
	default:
		return "error", "Unexpected code: " + strconv.Itoa(int(code)), "", ""
	}
	if t, _ := data["refresh_token"].(string); strings.TrimSpace(t) != "" && len(strings.TrimSpace(t)) <= 512 {
		refresh = strings.TrimSpace(t)
	}
	jar := loginCookies(res)
	if _, ok := jar["SESSDATA"]; !ok {
		if raw, _ := data["url"].(string); raw != "" {
			if u, err := url.Parse(raw); err == nil && u.Scheme == "https" && u.Host == "passport.biligame.com" &&
				u.EscapedPath() == "/x/passport-login/web/crossDomain" && u.User == nil {
				if r2, _, err := biliGet(ctx, biliNoRedirect, raw, ""); err == nil {
					for k, v := range loginCookies(r2) {
						jar[k] = v
					}
				}
			}
		}
	}
	var parts []string
	for _, n := range biliLoginCookies {
		if v, ok := jar[n]; ok {
			parts = append(parts, n+"="+v)
		}
	}
	cookies = strings.Join(parts, "; ")
	if !strings.Contains(cookies, "SESSDATA=") {
		return "error", "Missing SESSDATA in response", "", ""
	}
	return "confirmed", "", cookies, refresh
}

// sessDataExpiry reads the expiry SESSDATA carries after its first comma, in epoch seconds.
func sessDataExpiry(cookies string) int64 {
	for _, p := range strings.Split(cookies, ";") {
		p = strings.TrimSpace(p)
		if len(p) > 9 && strings.EqualFold(p[:9], "SESSDATA=") {
			parts := strings.Split(p[9:], ",")
			if len(parts) > 1 {
				if ts, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64); err == nil && ts >= 1_000_000_000 {
					return ts * 1000
				}
			}
			return 0
		}
	}
	return 0
}

func (s *Server) bilibiliSessionRoutes(rt *Router) {
	rt.Handle("POST", "/bilibili-session/qr", s.authed(func(w http.ResponseWriter, r *http.Request, _ string) {
		if s.biliCrypto() == nil {
			httpx.Error(w, r, http.StatusServiceUnavailable, "BiliBili Session is unavailable", "bilibili_session_unavailable")
			return
		}
		_, root, err := biliGet(r.Context(), biliNoRedirect, "https://passport.bilibili.com/x/passport-login/web/qrcode/generate", "")
		msg := ""
		data, _ := root["data"].(map[string]any)
		u, _ := data["url"].(string)
		key, _ := data["qrcode_key"].(string)
		switch {
		case err != nil:
			msg = err.Error()
		case root == nil:
			msg = "Invalid JSON"
		case data == nil:
			msg = "Missing data"
		case u == "":
			msg = "Missing QR URL"
		case key == "":
			msg = "Missing QR key"
		}
		if msg != "" {
			httpx.Error(w, r, http.StatusBadGateway, msg, "bilibili_qr_error")
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"qrUrl": u, "qrcodeKey": key, "expiresAt": time.Now().UnixMilli() + 180_000})
	}))
	rt.Handle("POST", "/bilibili-session/qr/poll", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			Key *string `json:"qrcodeKey"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req) != nil || req.Key == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		if strings.TrimSpace(*req.Key) == "" {
			plain(w, r, http.StatusBadRequest, "Missing qrcodeKey")
			return
		}
		c := s.biliCrypto()
		if c == nil {
			httpx.JSON(w, http.StatusOK, map[string]string{"status": "unavailable", "message": "BiliBili session is unavailable"})
			return
		}
		status, msg, cookies, refresh := biliQRPoll(r.Context(), *req.Key)
		if status == "confirmed" {
			expires := sessDataExpiry(cookies)
			if expires == 0 {
				expires = time.Now().UnixMilli() + 30*24*3600*1000
			}
			var encRefresh *string
			if refresh != "" {
				e := c.encrypt(refresh)
				encRefresh = &e
			}
			now := time.Now().UnixMilli()
			if _, err := s.DB.Exec(r.Context(), `INSERT INTO bilibili_sessions (user_id, encrypted_cookies, status, created_at, updated_at, last_used_at, expires_at,
encrypted_refresh_token, encrypted_pending_confirm_token, refresh_checked_at, refresh_claimed_until) VALUES ($1, $2, 'connected', $3, $3, 0, $4, $5, NULL, 0, 0)
ON CONFLICT (user_id) DO UPDATE SET encrypted_cookies = EXCLUDED.encrypted_cookies, encrypted_refresh_token = EXCLUDED.encrypted_refresh_token,
encrypted_pending_confirm_token = NULL, status = 'connected', updated_at = EXCLUDED.updated_at, last_used_at = 0, expires_at = EXCLUDED.expires_at,
refresh_checked_at = 0, refresh_claimed_until = 0`, user, c.encrypt(cookies), now, expires, encRefresh); err != nil {
				s.fail(w, r, err)
				return
			}
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": status, "message": msg})
	}))
	rt.Handle("GET", "/bilibili-session/status", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		out := map[string]any{"status": "disconnected", "updatedAt": 0, "lastUsedAt": 0, "expiresAt": 0}
		if s.biliCrypto() != nil {
			var status string
			var updated, used, expires int64
			var refresh *string
			err := s.DB.QueryRow(r.Context(), "SELECT status, updated_at, last_used_at, expires_at, encrypted_refresh_token FROM bilibili_sessions WHERE user_id = $1", user).
				Scan(&status, &updated, &used, &expires, &refresh)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				s.fail(w, r, err)
				return
			default:
				if status != "connected" && status != "needs_reconnect" {
					status = "disconnected"
				}
				if status == "connected" && expires > 0 && expires < time.Now().UnixMilli() && refresh == nil {
					s.biliNeedsReconnect(r.Context(), user)
					status = "needs_reconnect"
				}
				out = map[string]any{"status": status, "updatedAt": updated, "lastUsedAt": used, "expiresAt": expires}
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("GET", "/bilibili-session/health", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": s.biliHealth(r.Context(), user)})
	}))
	rt.Handle("DELETE", "/bilibili-session", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if _, err := s.DB.Exec(r.Context(), "DELETE FROM bilibili_sessions WHERE user_id = $1", user); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

func (s *Server) biliNeedsReconnect(ctx context.Context, user string) {
	now := time.Now().UnixMilli()
	s.DB.Exec(ctx, `UPDATE bilibili_sessions SET status = 'needs_reconnect', updated_at = $2, last_used_at = $2, encrypted_refresh_token = NULL,
encrypted_pending_confirm_token = NULL, refresh_claimed_until = 0 WHERE user_id = $1`, user, now)
}

// biliHealth is BiliBiliSessionService.healthCheck: the nav API with the stored cookies.
func (s *Server) biliHealth(ctx context.Context, user string) string {
	c := s.biliCrypto()
	if c == nil {
		return "unconfigured"
	}
	cookies := s.biliAccountCookies(ctx, user)
	if cookies == "" {
		return "disconnected"
	}
	res, root, err := biliGet(ctx, &http.Client{Timeout: 15 * time.Second}, "https://api.bilibili.com/x/web-interface/nav", cookies)
	if err != nil {
		return "error"
	}
	if res.StatusCode == 412 {
		return "rate_limited"
	}
	code, ok := root["code"].(float64)
	if !ok {
		return "error"
	}
	switch int(code) {
	case 0:
		return "healthy"
	case -101:
		return "expired"
	case -352:
		return "rate_limited"
	}
	return "error"
}

// biliAccountCookies is BiliBiliSessionService.connectedCookies: the decrypted cookies of a connected session; a
// session that no longer decrypts is marked for reconnection.
func (s *Server) biliAccountCookies(ctx context.Context, user string) string {
	c := s.biliCrypto()
	if c == nil {
		return ""
	}
	var enc string
	if err := s.DB.QueryRow(ctx, "SELECT encrypted_cookies FROM bilibili_sessions WHERE user_id = $1 AND status = 'connected'", user).Scan(&enc); err != nil {
		return ""
	}
	cookies, err := c.decrypt(enc)
	if err != nil {
		s.biliNeedsReconnect(ctx, user)
		return ""
	}
	return cookies
}

// accountRate is BiliBiliSessionScope.checkRateLimit: 30 signed-in extractions a minute per user.
type accountRate struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

var biliAccountRate = &accountRate{hits: map[string][]time.Time{}}

func (a *accountRate) allow(user string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	q := a.hits[user]
	for len(q) > 0 && now.Sub(q[0]) > time.Minute {
		q = q[1:]
	}
	if len(q) >= 30 {
		a.hits[user] = q
		return false
	}
	a.hits[user] = append(q, now)
	return true
}
