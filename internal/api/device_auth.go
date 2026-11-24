package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"typetype.dev/server/internal/httpx"
)

// Device authorization lets a TV sign in by showing a code a phone approves. The keys and stored values are the
// Kotlin ones (HMAC with the JWT secret, never a code in clear), so a sign-in started on one server finishes on the
// other.
const (
	deviceLifetime  = 600
	deviceInterval  = 2
	deviceFailLimit = 5
	deviceStartMax  = 20
	deviceLock      = 900 * time.Second
	devicePollGapMs = 1500
	userCodeChars   = "BCDFGHJKLMNPQRSTVWXZ23456789"
	devicePrefix    = "device-auth:v1"
)

var deviceCodeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type deviceRequest struct {
	DeviceName  string `json:"deviceName"`
	UserCodeKey string `json:"userCodeKey"`
	CreatedAt   int64  `json:"createdAt"`
	ExpiresAt   int64  `json:"expiresAt"`
}

func (s *Server) deviceHash(v string) string {
	m := hmac.New(sha256.New, []byte("device-authorization:"+s.Cfg.JWTSecret))
	m.Write([]byte(v))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) dkey(kind, v string) string { return devicePrefix + ":" + kind + ":" + v }

func randomUserCode() string {
	b := make([]byte, 8)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(userCodeChars))))
		b[i] = userCodeChars[n.Int64()]
	}
	return string(b)
}

func displayCode(c string) string { return c[:4] + "-" + c[4:] }

func normalizeUserCode(in string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		if r != '-' && !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	c := b.String()
	if len(c) != 8 || strings.Trim(c, userCodeChars) != "" {
		return ""
	}
	return c
}

func cleanDeviceName(in string) string {
	var b strings.Builder
	for _, r := range in {
		if unicode.IsControl(r) {
			r = ' '
		}
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	name := strings.Join(strings.Fields(b.String()), " ")
	if rs := []rune(name); len(rs) > 64 {
		name = string(rs[:64])
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "TypeType TV"
	}
	return name
}

func deviceError(w http.ResponseWriter, r *http.Request, status int, code string) {
	httpx.Error(w, r, status, strings.ReplaceAll(code, "_", " "), code)
}

func (s *Server) deviceEnabled(ctx context.Context) bool {
	st, err := s.Admin.Get(ctx)
	return err == nil && st.DeviceLoginEnabled
}

func (s *Server) loadDevice(ctx context.Context, hash string) (*deviceRequest, bool) {
	v, ok, _ := s.Cache.Get(ctx, s.dkey("request", hash))
	if !ok {
		return nil, false
	}
	var d deviceRequest
	if json.Unmarshal([]byte(v), &d) != nil {
		return nil, false
	}
	return &d, true
}

func remaining(d *deviceRequest) time.Duration {
	return time.Duration(max(1, (d.ExpiresAt-time.Now().UnixMilli())/1000)) * time.Second
}

func (s *Server) forgetDevice(ctx context.Context, hash string, d *deviceRequest) {
	for _, k := range []string{s.dkey("request", hash), d.UserCodeKey, s.dkey("decision", hash), s.dkey("scanned", hash), s.dkey("poll", hash)} {
		s.Cache.Delete(ctx, k)
	}
}

func (s *Server) deviceRoutes(rt *Router) {
	rt.Handle("POST", "/auth/device/start", func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		var req struct {
			DeviceName string `json:"deviceName"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<14))
		json.Unmarshal(body, &req)
		ctx := r.Context()
		if !s.deviceEnabled(ctx) {
			deviceError(w, r, http.StatusForbidden, "device_login_disabled")
			return
		}
		if n, err := s.Cache.Increment(ctx, s.dkey("starts", s.deviceHash(httpx.ClientIP(r))), deviceLifetime*time.Second); err == nil && n > deviceStartMax {
			deviceError(w, r, http.StatusTooManyRequests, "rate_limited")
			return
		}
		raw := make([]byte, 32)
		rand.Read(raw)
		code := base64.RawURLEncoding.EncodeToString(raw)
		hash := s.deviceHash(code)
		user := ""
		for i := 0; i < 5 && user == ""; i++ {
			c := randomUserCode()
			if ok, err := s.Cache.SetIfAbsent(ctx, s.dkey("user-code", s.deviceHash(c)), hash, deviceLifetime*time.Second); err == nil && ok {
				user = c
			}
		}
		if user == "" {
			s.fail(w, r, io.ErrUnexpectedEOF)
			return
		}
		now := time.Now().UnixMilli()
		d := deviceRequest{DeviceName: cleanDeviceName(req.DeviceName), UserCodeKey: s.dkey("user-code", s.deviceHash(user)), CreatedAt: now,
			ExpiresAt: now + deviceLifetime*1000}
		b, _ := json.Marshal(d)
		s.Cache.Set(ctx, s.dkey("request", hash), string(b), deviceLifetime*time.Second)
		httpx.JSON(w, http.StatusOK, map[string]any{"deviceCode": code, "userCode": displayCode(user), "expiresIn": deviceLifetime, "interval": deviceInterval})
	})
	decision := func(kind string) http.HandlerFunc {
		return s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
			noStore(w)
			code := r.URL.Query().Get("code")
			if kind != "lookup" {
				var req struct {
					UserCode *string `json:"userCode"`
				}
				body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<14))
				if json.Unmarshal(body, &req) != nil || req.UserCode == nil {
					deviceError(w, r, http.StatusBadRequest, "invalid_request")
					return
				}
				code = *req.UserCode
			}
			s.deviceDecision(w, r, user, kind, code)
		})
	}
	rt.Handle("GET", "/auth/device/lookup", decision("lookup"))
	rt.Handle("POST", "/auth/device/approve", decision("approve"))
	rt.Handle("POST", "/auth/device/deny", decision("deny"))
	rt.Handle("POST", "/auth/device/token", s.deviceToken)
}

func (s *Server) deviceDecision(w http.ResponseWriter, r *http.Request, user, kind, code string) {
	ctx := r.Context()
	if !s.deviceEnabled(ctx) {
		deviceError(w, r, http.StatusForbidden, "device_login_disabled")
		return
	}
	if strings.HasPrefix(user, "guest:") {
		deviceError(w, r, http.StatusForbidden, "guest_forbidden")
		return
	}
	client := httpx.ClientIP(r)
	acct, cl := s.dkey("failures:account", s.deviceHash(user)), s.dkey("failures:client", s.deviceHash(client))
	count := func(k string) int64 {
		v, _, _ := s.Cache.Get(ctx, k)
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	if count(acct) >= deviceFailLimit || count(cl) >= deviceFailLimit {
		deviceError(w, r, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	fail := func() {
		s.Cache.Increment(ctx, acct, deviceLock)
		s.Cache.Increment(ctx, cl, deviceLock)
		deviceError(w, r, http.StatusNotFound, "code_not_found")
	}
	norm := normalizeUserCode(code)
	if norm == "" {
		fail()
		return
	}
	hash, ok, _ := s.Cache.Get(ctx, s.dkey("user-code", s.deviceHash(norm)))
	if !ok {
		fail()
		return
	}
	d, ok := s.loadDevice(ctx, hash)
	if !ok || time.Now().UnixMilli() >= d.ExpiresAt {
		fail()
		return
	}
	if _, decided, _ := s.Cache.Get(ctx, s.dkey("decision", hash)); decided {
		deviceError(w, r, http.StatusConflict, "already_decided")
		return
	}
	switch kind {
	case "lookup":
		s.Cache.Set(ctx, s.dkey("scanned", hash), "1", remaining(d))
		httpx.JSON(w, http.StatusOK, map[string]any{"userCode": displayCode(norm), "deviceName": d.DeviceName, "requestedAt": d.CreatedAt, "expiresAt": d.ExpiresAt})
		return
	case "approve":
		owner, _ := s.Auth.Profiles.OwnerID(ctx, user)
		if owner == "" {
			owner = user
		}
		if ok, _ := s.Cache.SetIfAbsent(ctx, s.dkey("decision", hash), "approved:"+owner, remaining(d)); !ok {
			deviceError(w, r, http.StatusConflict, "already_decided")
			return
		}
	default:
		if ok, _ := s.Cache.SetIfAbsent(ctx, s.dkey("decision", hash), "denied", remaining(d)); !ok {
			deviceError(w, r, http.StatusConflict, "already_decided")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deviceToken(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	var req struct {
		DeviceCode *string `json:"deviceCode"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<14))
	if json.Unmarshal(body, &req) != nil || req.DeviceCode == nil {
		deviceError(w, r, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx := r.Context()
	if !s.deviceEnabled(ctx) {
		deviceError(w, r, http.StatusForbidden, "device_login_disabled")
		return
	}
	if !deviceCodeRe.MatchString(*req.DeviceCode) {
		deviceError(w, r, http.StatusBadRequest, "invalid_request")
		return
	}
	hash := s.deviceHash(*req.DeviceCode)
	d, ok := s.loadDevice(ctx, hash)
	if !ok {
		deviceError(w, r, http.StatusBadRequest, "expired_token")
		return
	}
	now := time.Now().UnixMilli()
	if now >= d.ExpiresAt {
		s.forgetDevice(ctx, hash, d)
		deviceError(w, r, http.StatusBadRequest, "expired_token")
		return
	}
	prev, had, _ := s.Cache.Get(ctx, s.dkey("poll", hash))
	s.Cache.Set(ctx, s.dkey("poll", hash), strconv.FormatInt(now, 10), remaining(d))
	if p, err := strconv.ParseInt(prev, 10, 64); had && err == nil && now-p < devicePollGapMs {
		deviceError(w, r, http.StatusBadRequest, "slow_down")
		return
	}
	dec, ok, _ := s.Cache.Get(ctx, s.dkey("decision", hash))
	if !ok {
		if _, scanned, _ := s.Cache.Get(ctx, s.dkey("scanned", hash)); scanned {
			deviceError(w, r, http.StatusBadRequest, "authorization_scanned")
		} else {
			deviceError(w, r, http.StatusBadRequest, "authorization_pending")
		}
		return
	}
	if _, ok, _ := s.Cache.GetAndDelete(ctx, s.dkey("request", hash)); !ok {
		deviceError(w, r, http.StatusBadRequest, "expired_token")
		return
	}
	s.forgetDevice(ctx, hash, d)
	owner, approved := strings.CutPrefix(dec, "approved:")
	if !approved {
		deviceError(w, r, http.StatusBadRequest, "access_denied")
		return
	}
	t, err := s.Auth.IssueForOwner(ctx, owner)
	if err != nil {
		deviceError(w, r, http.StatusBadRequest, "access_denied")
		return
	}
	s.signedIn(w, t)
}
