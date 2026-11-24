package api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/httpx"
)

// secretConfig is SecretConfigReader.read: NAME, else the file NAME_FILE points at, placeholders ignored.
func secretConfig(name string) string {
	placeholder := map[string]bool{"SET_ME_SHARED_SECRET": true, "SET_ME_YOUTUBE_REMOTE_LOGIN_INTERNAL_TOKEN": true,
		"SET_ME_YOUTUBE_SESSION_ENCRYPTION_KEY": true, "SET_ME_RECOMMENDER_INTERNAL_TOKEN": true, "replace-with-shared-internal-token": true,
		"replace-with-at-least-32-random-characters": true}
	clean := func(v string) string {
		v = strings.TrimSpace(v)
		if placeholder[v] {
			return ""
		}
		return v
	}
	if v := clean(os.Getenv(name)); v != "" {
		return v
	}
	if p := clean(os.Getenv(name + "_FILE")); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			return clean(string(b))
		}
	}
	return ""
}

// sessionCrypto is YoutubeSessionCrypto (and BiliBiliSessionCrypto with its own label): AES-256-GCM keyed by
// sha256 of the secret, a 12-byte nonce, the label as associated data, "gcm256." + base64url without padding.
type sessionCrypto struct {
	aead cipher.AEAD
	aad  []byte
}

func newSessionCrypto(secret, label string) *sessionCrypto {
	if len(secret) < 32 {
		return nil
	}
	key := sha256.Sum256([]byte(secret))
	block, _ := aes.NewCipher(key[:])
	aead, _ := cipher.NewGCM(block)
	return &sessionCrypto{aead, []byte(label)}
}

func (c *sessionCrypto) encrypt(v string) string {
	nonce := make([]byte, 12)
	rand.Read(nonce)
	return "gcm256." + base64.RawURLEncoding.EncodeToString(c.aead.Seal(nonce, nonce, []byte(v), c.aad))
}

func (c *sessionCrypto) decrypt(v string) (string, error) {
	raw, ok := strings.CutPrefix(v, "gcm256.")
	if !ok {
		return "", errors.New("Invalid encrypted payload format")
	}
	b, err := decodeB64URL(raw)
	if err != nil || len(b) <= 12 {
		return "", errors.New("Invalid encrypted payload")
	}
	out, err := c.aead.Open(nil, b[:12], b[12:], c.aad)
	return string(out), err
}

var allowedYoutubeCookies = map[string]bool{"SID": true, "HSID": true, "SSID": true, "APISID": true, "SAPISID": true, "LOGIN_INFO": true,
	"SIDCC": true, "__Secure-1PSID": true, "__Secure-3PSID": true, "__Secure-1PAPISID": true, "__Secure-3PAPISID": true, "__Secure-1PSIDCC": true,
	"__Secure-3PSIDCC": true, "__Secure-1PSIDTS": true, "__Secure-3PSIDTS": true, "__Host-1PLSID": true, "__Host-3PLSID": true, "AEC": true,
	"NID": true, "PREF": true, "SOCS": true, "VISITOR_INFO1_LIVE": true, "VISITOR_PRIVACY_METADATA": true, "YSC": true}

// normalizeYoutubeCookies is YoutubeSessionCookieNormalizer: a Cookie header or a Netscape cookies.txt, reduced to
// the YouTube and Google session cookies, youtube.com winning over google.com for the same name.
func normalizeYoutubeCookies(raw string) string {
	if len(raw) > 1<<20 {
		return ""
	}
	type cookie struct {
		value    string
		priority int
	}
	var order []string
	jar := map[string]cookie{}
	put := func(name, value string, prio int) {
		cur, ok := jar[name]
		if !ok {
			order = append(order, name)
		}
		if !ok || prio >= cur.priority {
			jar[name] = cookie{value, prio}
		}
	}
	if strings.Contains(raw, "\t") {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSuffix(line, "\r")
			httpOnly := strings.HasPrefix(line, "#HttpOnly_")
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") && !httpOnly {
				continue
			}
			f := strings.Split(line, "\t")
			if len(f) < 7 {
				continue
			}
			d := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(f[0], "#HttpOnly_")), "."))
			prio := 0
			switch {
			case d == "youtube.com" || strings.HasSuffix(d, ".youtube.com"):
				prio = 4
			case d == "google.com" || strings.HasSuffix(d, ".google.com"):
				prio = 3
			case d == "youtube-nocookie.com" || strings.HasSuffix(d, ".youtube-nocookie.com"):
				prio = 2
			default:
				continue
			}
			put(strings.TrimSpace(f[5]), strings.TrimSpace(f[6]), prio)
		}
	} else {
		h := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "Cookie:"))
		for _, part := range strings.Split(h, ";") {
			i := strings.IndexByte(part, '=')
			if i <= 0 {
				continue
			}
			put(strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:]), 10)
		}
	}
	var parts []string
	for _, n := range order {
		if c := jar[n]; allowedYoutubeCookies[n] && strings.TrimSpace(c.value) != "" {
			parts = append(parts, n+"="+c.value)
		}
	}
	return strings.Join(parts, "; ")
}

func hasCookie(cookies, name string) bool {
	for _, p := range strings.Split(cookies, ";") {
		t := strings.TrimSpace(p)
		if strings.HasPrefix(t, name+"=") && len(t) > len(name)+1 {
			return true
		}
	}
	return false
}

// validYoutubeCredentials is YoutubeSessionCredentialValidator plus the auth user range.
func validYoutubeCredentials(cookies, poToken string, authUser int) bool {
	if authUser < 0 || authUser > 99 || strings.TrimSpace(cookies) == "" || strings.TrimSpace(poToken) == "" || len(cookies) > 32<<10 ||
		len(poToken) > 16<<10 || len(poToken) < 8 {
		return false
	}
	return (hasCookie(cookies, "SID") || hasCookie(cookies, "__Secure-1PSID") || hasCookie(cookies, "__Secure-3PSID")) &&
		(hasCookie(cookies, "SAPISID") || hasCookie(cookies, "__Secure-3PAPISID"))
}

func (s *Server) youtubeCrypto() *sessionCrypto {
	return newSessionCrypto(secretConfig("YOUTUBE_SESSION_ENCRYPTION_KEY"), "typetype.youtube-session")
}

// tokenCall posts {field: value} to the token service, answering the JSON body or nil when it fails.
func (s *Server) tokenCall(ctx context.Context, path string, body map[string]any) map[string]any {
	base := strings.TrimRight(s.Cfg.YoutubeTokenURL, "/")
	if base == "" {
		return nil
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	res, err := tokenHTTP.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	out := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 && json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

const youtubeSessionUpsert = `INSERT INTO youtube_sessions (user_id, encrypted_cookies, encrypted_po_token, auth_user, status, created_at, updated_at, last_used_at)
VALUES ($1, $2, $3, $4, 'connected', $5, $5, 0) ON CONFLICT (user_id) DO UPDATE SET encrypted_cookies = EXCLUDED.encrypted_cookies,
encrypted_po_token = EXCLUDED.encrypted_po_token, auth_user = EXCLUDED.auth_user, status = 'connected', updated_at = EXCLUDED.updated_at, last_used_at = 0`

func (s *Server) youtubeSessionRoutes(rt *Router) {
	unavailable := func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusServiceUnavailable, "YouTube Session is unavailable", "youtube_session_unavailable")
	}
	rt.Handle("POST", "/youtube-session/pairing", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if s.youtubeCrypto() == nil {
			unavailable(w, r)
			return
		}
		now := time.Now().UnixMilli()
		expires := now + 5*60*1000
		s.DB.Exec(r.Context(), "DELETE FROM youtube_session_pairings WHERE expires_at <= $1", now)
		for range 10 {
			code := pairingCode()
			tag, err := s.DB.Exec(r.Context(), "INSERT INTO youtube_session_pairings (code, user_id, created_at, expires_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING",
				code, user, now, expires)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if tag.RowsAffected() == 1 {
				httpx.JSON(w, http.StatusCreated, map[string]any{"code": code, "expiresAt": expires})
				return
			}
		}
		s.fail(w, r, errors.New("Unable to allocate YouTube session pairing code"))
	}))
	rt.Handle("POST", "/youtube-session/import", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		c := s.youtubeCrypto()
		if c == nil {
			unavailable(w, r)
			return
		}
		var req struct {
			Cookies *string `json:"cookies"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req) != nil || req.Cookies == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		invalid := func() {
			httpx.Error(w, r, http.StatusUnauthorized, "YouTube rejected the imported session", "youtube_session_invalid")
		}
		binding := s.tokenCall(r.Context(), "/session-binding", map[string]any{"cookies": *req.Cookies})
		if binding == nil {
			invalid()
			return
		}
		str := func(k string) string { v, _ := binding[k].(string); return v }
		authUser := 0
		if f, ok := binding["authUser"].(float64); ok {
			authUser = int(f)
		}
		if str("cookies") == "" || str("poToken") == "" {
			invalid()
			return
		}
		cookies, poToken := normalizeYoutubeCookies(str("cookies")), strings.TrimSpace(str("poToken"))
		if cookies == "" || !validYoutubeCredentials(cookies, poToken, authUser) {
			invalid()
			return
		}
		if _, err := s.DB.Exec(r.Context(), youtubeSessionUpsert, user, c.encrypt(cookies), c.encrypt(poToken), authUser, time.Now().UnixMilli()); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("POST", "/youtube-session/complete", func(w http.ResponseWriter, r *http.Request) {
		c := s.youtubeCrypto()
		if c == nil {
			unavailable(w, r)
			return
		}
		var req struct {
			Code     *string `json:"code"`
			Cookies  *string `json:"cookies"`
			PoToken  *string `json:"poToken"`
			AuthUser int     `json:"authUser"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req) != nil || req.Code == nil || req.Cookies == nil || req.PoToken == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		code := strings.ToUpper(strings.TrimSpace(*req.Code))
		cookies, poToken := normalizeYoutubeCookies(*req.Cookies), strings.TrimSpace(*req.PoToken)
		if cookies == "" || code == "" || !validYoutubeCredentials(cookies, poToken, req.AuthUser) {
			httpx.Error(w, r, http.StatusBadRequest, "Invalid YouTube credentials", "youtube_credentials_invalid")
			return
		}
		now := time.Now().UnixMilli()
		var user string
		var expires int64
		err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			if err := tx.QueryRow(r.Context(), "SELECT user_id, expires_at FROM youtube_session_pairings WHERE code = $1", code).Scan(&user, &expires); err != nil {
				return err
			}
			if expires <= now {
				_, err := tx.Exec(r.Context(), "DELETE FROM youtube_session_pairings WHERE code = $1", code)
				user = ""
				return err
			}
			if _, err := tx.Exec(r.Context(), youtubeSessionUpsert, user, c.encrypt(cookies), c.encrypt(poToken), req.AuthUser, now); err != nil {
				return err
			}
			_, err := tx.Exec(r.Context(), "DELETE FROM youtube_session_pairings WHERE code = $1", code)
			return err
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			httpx.Error(w, r, http.StatusNotFound, "Invalid pairing code", "youtube_pairing_invalid")
		case err != nil:
			s.fail(w, r, err)
		case user == "":
			httpx.Error(w, r, http.StatusGone, "Pairing code expired", "youtube_pairing_expired")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	rt.Handle("GET", "/youtube-session/status", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		out := map[string]any{"status": "disconnected", "updatedAt": 0, "lastUsedAt": 0, "authType": "cookies", "accountLabel": ""}
		if s.youtubeCrypto() != nil {
			var status, authType, label string
			var updated, used int64
			err := s.DB.QueryRow(r.Context(), "SELECT status, updated_at, last_used_at, auth_type, account_label FROM youtube_sessions WHERE user_id = $1", user).
				Scan(&status, &updated, &used, &authType, &label)
			if err == nil {
				if status != "connected" && status != "needs_reconnect" {
					status = "disconnected"
				}
				out = map[string]any{"status": status, "updatedAt": updated, "lastUsedAt": used, "authType": authType, "accountLabel": label}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				s.fail(w, r, err)
				return
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	deviceReady := func() bool { return s.youtubeCrypto() != nil && s.Cfg.YoutubeTokenURL != "" }
	rt.Handle("POST", "/youtube-session/device/start", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !deviceReady() {
			unavailable(w, r)
			return
		}
		p := s.tokenCall(r.Context(), "/device/start", map[string]any{"accountKey": user})
		code, _ := p["userCode"].(string)
		verify, _ := p["verificationUrl"].(string)
		if p == nil || strings.TrimSpace(code) == "" || strings.TrimSpace(verify) == "" {
			httpx.Error(w, r, http.StatusBadGateway, "Could not start the YouTube device login", "youtube_device_unavailable")
			return
		}
		expires, _ := p["expiresAt"].(float64)
		interval, _ := p["intervalSeconds"].(float64)
		httpx.JSON(w, http.StatusCreated, map[string]any{"userCode": code, "verificationUrl": verify, "expiresAt": int64(expires), "intervalSeconds": int(interval)})
	}))
	rt.Handle("GET", "/youtube-session/device/status", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		status := s.pollYoutubeDevice(r.Context(), user)
		switch status {
		case "connected", "pending", "slow_down":
			httpx.JSON(w, http.StatusOK, map[string]string{"status": status})
		case "denied":
			httpx.Error(w, r, http.StatusGone, "The YouTube authorization was denied", "youtube_device_denied")
		case "expired":
			httpx.Error(w, r, http.StatusGone, "The YouTube device code expired", "youtube_device_expired")
		default:
			httpx.Error(w, r, http.StatusGone, "The YouTube device login was lost, start again", "youtube_device_expired")
		}
	}))
	rt.Handle("POST", "/youtube-session/device/cancel", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		s.tokenCall(r.Context(), "/device/cancel", map[string]any{"accountKey": user})
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("DELETE", "/youtube-session", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if c := s.youtubeCrypto(); c != nil {
			var refresh string
			if s.DB.QueryRow(r.Context(), "SELECT encrypted_refresh_token FROM youtube_sessions WHERE user_id = $1 AND auth_type = 'oauth_device' AND status = 'connected'",
				user).Scan(&refresh) == nil {
				if t, err := c.decrypt(refresh); err == nil {
					s.tokenCall(r.Context(), "/device/revoke", map[string]any{"token": t})
				}
			}
		}
		if _, err := s.DB.Exec(r.Context(), "DELETE FROM youtube_sessions WHERE user_id = $1", user); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// pollYoutubeDevice is YoutubeSessionService.pollDeviceLogin: an authorized login stores its tokens encrypted.
func (s *Server) pollYoutubeDevice(ctx context.Context, user string) string {
	c := s.youtubeCrypto()
	if c == nil || s.Cfg.YoutubeTokenURL == "" {
		return "unavailable"
	}
	p := s.tokenCall(ctx, "/device/poll", map[string]any{"accountKey": user})
	if p == nil {
		return "unavailable"
	}
	status, _ := p["status"].(string)
	if status != "authorized" {
		return status
	}
	tokens, _ := p["tokens"].(map[string]any)
	if tokens == nil {
		return "unavailable"
	}
	access, _ := tokens["accessToken"].(string)
	refresh, _ := tokens["refreshToken"].(string)
	expiresIn, _ := tokens["expiresIn"].(float64)
	if strings.TrimSpace(refresh) == "" {
		return "invalid"
	}
	now := time.Now().UnixMilli()
	if _, err := s.DB.Exec(ctx, `INSERT INTO youtube_sessions (user_id, encrypted_cookies, encrypted_po_token, encrypted_access_token, encrypted_refresh_token,
access_expires_at, account_label, auth_type, status, created_at, updated_at, last_used_at) VALUES ($1, '', '', $2, $3, $4, '', 'oauth_device', 'connected', $5, $5, 0)
ON CONFLICT (user_id) DO UPDATE SET encrypted_cookies = '', encrypted_po_token = '', encrypted_access_token = EXCLUDED.encrypted_access_token,
encrypted_refresh_token = EXCLUDED.encrypted_refresh_token, access_expires_at = EXCLUDED.access_expires_at, account_label = '', auth_type = 'oauth_device',
status = 'connected', updated_at = EXCLUDED.updated_at, last_used_at = 0`, user, c.encrypt(access), c.encrypt(refresh), now+int64(expiresIn)*1000, now); err != nil {
		return "unavailable"
	}
	return "connected"
}

const pairingAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func pairingCode() string {
	var b strings.Builder
	for range 8 {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(pairingAlphabet))))
		b.WriteByte(pairingAlphabet[n.Int64()])
	}
	return b.String()
}

// tokenHTTP bounds every call to TypeType-Token, as TypetypeTokenHttpClient.bounded does.
var tokenHTTP = &http.Client{Timeout: 20 * time.Second}
