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
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"typetype.dev/server/internal/config"
	"typetype.dev/server/internal/httpx"
)

// twitch relays the Twitch routes to TypeType-Live, which owns every call to Twitch; this side only checks
// parameters and keeps each user's Twitch tokens, encrypted like TwitchTokenCrypto so stored rows stay readable.
type twitch struct {
	base, secret string
	gcm          cipher.AEAD
	calls        *http.Client
	streams      *http.Client
}

const twitchAAD = "typetype.twitch-token"

func newTwitch() *twitch {
	base, secret, key := config.TrimmedURL("TWITCH_SERVICE_URL"), config.Secret("TWITCH_SERVICE_SECRET"), config.Secret("TWITCH_TOKEN_ENCRYPTION_KEY")
	if base == "" || secret == "" || len(key) < 32 {
		return nil
	}
	sum := sha256.Sum256([]byte(key))
	block, _ := aes.NewCipher(sum[:])
	gcm, _ := cipher.NewGCM(block)
	timeout := time.Duration(config.IntEnv("TWITCH_SERVICE_TIMEOUT_MS", 15000)) * time.Millisecond
	return &twitch{base: base, secret: secret, gcm: gcm, calls: &http.Client{Timeout: timeout}, streams: &http.Client{}}
}

func (t *twitch) encrypt(v string) string {
	nonce := make([]byte, 12)
	rand.Read(nonce)
	return "gcm256." + base64.RawURLEncoding.EncodeToString(append(nonce, t.gcm.Seal(nil, nonce, []byte(v), []byte(twitchAAD))...))
}

func (t *twitch) decrypt(v string) (string, error) {
	raw, ok := strings.CutPrefix(v, "gcm256.")
	if !ok {
		return "", errors.New("invalid encrypted payload format")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "="))
	if err != nil || len(b) <= 12 {
		return "", errors.New("invalid encrypted payload")
	}
	out, err := t.gcm.Open(nil, b[:12], b[12:], []byte(twitchAAD))
	return string(out), err
}

type liveResponse struct {
	status int
	body   []byte
}

func (t *twitch) do(ctx context.Context, method, path string, query url.Values, body []byte, token string) liveResponse {
	u := t.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	} else if method == "PUT" || method == "POST" {
		rd = strings.NewReader("{}")
	}
	req, _ := http.NewRequestWithContext(ctx, method, u, rd)
	req.Header.Set("X-Live-Secret", t.secret)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Twitch-Token", token)
	}
	res, err := t.calls.Do(req)
	if err != nil {
		return liveResponse{http.StatusBadGateway, []byte(`{"error":"live_service_unavailable"}`)}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	return liveResponse{res.StatusCode, b}
}

type twitchAccount struct {
	AccessToken  string
	RefreshToken *string
	Login        string
	DisplayName  string
	AvatarURL    string
	ExpiresAt    int64
}

func (s *Server) twitchAccount(ctx context.Context, user string) (*twitchAccount, error) {
	var enc string
	var encRefresh *string
	a := &twitchAccount{}
	err := s.DB.QueryRow(ctx, `SELECT encrypted_access_token, encrypted_refresh_token, login, display_name, avatar_url, expires_at
FROM twitch_accounts WHERE user_id = $1 LIMIT 1`, user).Scan(&enc, &encRefresh, &a.Login, &a.DisplayName, &a.AvatarURL, &a.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if a.AccessToken, err = s.twitch.decrypt(enc); err != nil {
		return nil, err
	}
	if encRefresh != nil {
		r, err := s.twitch.decrypt(*encRefresh)
		if err == nil {
			a.RefreshToken = &r
		}
	}
	return a, nil
}

func (s *Server) saveTwitchAccount(ctx context.Context, user string, a *twitchAccount) error {
	var encRefresh *string
	if a.RefreshToken != nil {
		e := s.twitch.encrypt(*a.RefreshToken)
		encRefresh = &e
	}
	now := time.Now().UnixMilli()
	_, err := s.DB.Exec(ctx, `INSERT INTO twitch_accounts (user_id, encrypted_access_token, encrypted_refresh_token, login, display_name, avatar_url,
expires_at, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8) ON CONFLICT (user_id) DO UPDATE SET
encrypted_access_token = EXCLUDED.encrypted_access_token, encrypted_refresh_token = EXCLUDED.encrypted_refresh_token, login = EXCLUDED.login,
display_name = EXCLUDED.display_name, avatar_url = EXCLUDED.avatar_url, expires_at = EXCLUDED.expires_at, updated_at = EXCLUDED.updated_at`,
		user, s.twitch.encrypt(a.AccessToken), encRefresh, a.Login, a.DisplayName, a.AvatarURL, a.ExpiresAt, now)
	return err
}

func expiryFrom(m map[string]any) int64 {
	if n, ok := m["expiresIn"].(float64); ok && n > 0 {
		return time.Now().UnixMilli() + int64(n)*1000
	}
	return 0
}

func (s *Server) refreshTwitch(ctx context.Context, user string, a *twitchAccount) *twitchAccount {
	if a.RefreshToken == nil {
		return nil
	}
	b, _ := json.Marshal(map[string]string{"refreshToken": *a.RefreshToken})
	res := s.twitch.do(ctx, "POST", "/v1/auth/refresh", nil, b, "")
	var tok map[string]any
	if res.status != http.StatusOK || json.Unmarshal(res.body, &tok) != nil {
		return nil
	}
	access, _ := tok["accessToken"].(string)
	if access == "" {
		return nil
	}
	next := *a
	next.AccessToken, next.ExpiresAt = access, expiryFrom(tok)
	if r, ok := tok["refreshToken"].(string); ok {
		next.RefreshToken = &r
	}
	s.saveTwitchAccount(ctx, user, &next)
	return &next
}

// freshTwitch returns the user's account, refreshed when it expires within five minutes.
func (s *Server) freshTwitch(ctx context.Context, user string) *twitchAccount {
	a, err := s.twitchAccount(ctx, user)
	if err != nil {
		return nil
	}
	if a.ExpiresAt == 0 || a.ExpiresAt > time.Now().UnixMilli()+5*60*1000 {
		return a
	}
	if n := s.refreshTwitch(ctx, user, a); n != nil {
		return n
	}
	return a
}

func (s *Server) withTwitch(ctx context.Context, user string, call func(token string) liveResponse) (liveResponse, bool) {
	a := s.freshTwitch(ctx, user)
	if a == nil {
		return liveResponse{}, false
	}
	res := call(a.AccessToken)
	if res.status != http.StatusUnauthorized {
		return res, true
	}
	if n := s.refreshTwitch(ctx, user, a); n != nil {
		return call(n.AccessToken), true
	}
	return res, true
}

func writeLive(w http.ResponseWriter, res liveResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.status)
	w.Write(res.body)
}

var (
	twitchLoginRe = regexp.MustCompile(`^[a-zA-Z0-9_]{1,25}$`)
	twitchSlugRe  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
	twitchIDRe    = regexp.MustCompile(`^[0-9]{1,20}$`)
	twitchTagRe   = regexp.MustCompile(`^[\p{L}\p{Nd}]{1,25}$`)
	pollIDRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

func (s *Server) twitchOn(w http.ResponseWriter, r *http.Request) bool {
	if s.twitch != nil {
		return true
	}
	httpx.Error(w, r, http.StatusNotFound, "Twitch is not enabled on this instance", "twitch_disabled")
	return false
}

func pathOK(w http.ResponseWriter, r *http.Request, name string, re *regexp.Regexp) (string, bool) {
	v := r.PathValue(name)
	if !re.MatchString(v) {
		plain(w, r, http.StatusBadRequest, "Invalid "+name)
		return "", false
	}
	return v, true
}

func pickQuery(r *http.Request, names ...string) url.Values {
	out := url.Values{}
	for _, n := range names {
		if v, ok := r.URL.Query()[n]; ok {
			s := v[0]
			if len(s) > 200 {
				s = s[:200]
			}
			out.Set(n, s)
		}
	}
	return out
}

func (s *Server) twitchRoutes(rt *Router) {
	relay := func(path func(*http.Request) (string, string, bool), params []string, withUser bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p, name, ok := path(r)
			if !ok {
				plain(w, r, http.StatusBadRequest, "Invalid "+name)
				return
			}
			if !s.twitchOn(w, r) {
				return
			}
			user, ok := s.publicAccess(w, r)
			if !ok {
				return
			}
			token := ""
			if withUser && user != "" && !strings.HasPrefix(user, "guest:") {
				if a := s.freshTwitch(r.Context(), user); a != nil {
					token = a.AccessToken
				}
			}
			writeLive(w, s.twitch.do(r.Context(), "GET", p, pickQuery(r, params...), nil, token))
		}
	}
	static := func(p string) func(*http.Request) (string, string, bool) {
		return func(*http.Request) (string, string, bool) { return p, "", true }
	}
	param := func(name string, re *regexp.Regexp, tmpl string) func(*http.Request) (string, string, bool) {
		return func(r *http.Request) (string, string, bool) {
			v := r.PathValue(name)
			return strings.ReplaceAll(tmpl, "{}", v), name, re.MatchString(v)
		}
	}
	page := []string{"first", "after"}
	rt.Handle("GET", "/twitch/streams", relay(static("/v1/streams"), page, false))
	rt.Handle("GET", "/twitch/games", relay(static("/v1/games"), page, false))
	rt.Handle("GET", "/twitch/games/{slug}/streams", relay(param("slug", twitchSlugRe, "/v1/games/{}/streams"), page, false))
	rt.Handle("GET", "/twitch/tags/{tag}/streams", relay(param("tag", twitchTagRe, "/v1/tags/{}/streams"), page, false))
	rt.Handle("GET", "/twitch/channels/{login}", relay(param("login", twitchLoginRe, "/v1/channels/{}"), nil, false))
	rt.Handle("GET", "/twitch/channels/{login}/videos", relay(param("login", twitchLoginRe, "/v1/channels/{}/videos"), page, false))
	rt.Handle("GET", "/twitch/channels/{login}/clips", relay(param("login", twitchLoginRe, "/v1/channels/{}/clips"), page, false))
	rt.Handle("GET", "/twitch/live/{login}", relay(param("login", twitchLoginRe, "/v1/live/{}"), nil, true))
	rt.Handle("GET", "/twitch/videos/{id}", relay(param("id", twitchIDRe, "/v1/videos/{}"), nil, true))
	rt.Handle("GET", "/twitch/videos/{id}/comments", relay(param("id", twitchIDRe, "/v1/videos/{}/comments"), []string{"offset", "cursor"}, true))
	rt.Handle("GET", "/twitch/videos/{id}/chapters", relay(param("id", twitchIDRe, "/v1/videos/{}/chapters"), nil, true))
	rt.Handle("GET", "/twitch/clips/{slug}", relay(param("slug", twitchSlugRe, "/v1/clips/{}"), nil, false))
	rt.Handle("GET", "/twitch/search", relay(static("/v1/search"), []string{"q", "type", "first", "after"}, false))
	rt.Handle("GET", "/twitch/chat/assets", relay(static("/v1/chat/assets"), nil, false))
	rt.Handle("GET", "/twitch/chat/{login}/assets", relay(param("login", twitchLoginRe, "/v1/chat/{}/assets"), nil, false))
	rt.Handle("GET", "/twitch/chat/{login}/highlights", relay(param("login", twitchLoginRe, "/v1/chat/{}/highlights"), nil, false))
	s.twitchAccountRoutes(rt)
}
