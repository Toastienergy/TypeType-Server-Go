package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"typetype.dev/server/internal/config"
	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/sabr"
	"typetype.dev/server/internal/youtube"
)

type toFake struct{ base *url.URL }

func (f toFake) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host, r.Host = f.base.Scheme, f.base.Host, f.base.Host
	return http.DefaultTransport.RoundTrip(r)
}

// fakeTV stands in for the token service and YouTube's TVHTML5 player: it renews refresh-1 into access-2, refuses
// any other refresh token, deciphers n, and answers the player only for Bearer access-2.
func fakeTV(t *testing.T, refreshes *atomic.Int32, bearer *atomic.Value) *httptest.Server {
	write := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/device/refresh":
			refreshes.Add(1)
			if body["refreshToken"] != "refresh-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			write(w, map[string]any{"accessToken": "access-2", "expiresIn": 3600})
		case "/tv-token":
			write(w, map[string]any{"poToken": "cG90", "visitorData": "vis", "livingRoomPoTokenId": "room"})
		case "/youtube/player/decoder":
			out := map[string]any{"signatureTimestamp": 20779}
			if ns, _ := body["throttlingParameters"].([]any); len(ns) == 1 {
				out["throttlingParameters"] = map[string]string{ns[0].(string): "deciphered"}
			}
			write(w, out)
		case "/youtubei/v1/player":
			bearer.Store(r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") != "Bearer access-2" {
				write(w, map[string]any{"playabilityStatus": map[string]any{"status": "LOGIN_REQUIRED", "reason": "Sign in"}})
				return
			}
			write(w, map[string]any{
				"playabilityStatus": map[string]any{"status": "OK"},
				"streamingData": map[string]any{"serverAbrStreamingUrl": "https://rr1.googlevideo.com/videoplayback?expire=1900000000&n=raw",
					"adaptiveFormats": []any{map[string]any{"itag": 137, "mimeType": "video/mp4; codecs=\"avc1\"", "lastModified": "12"}}},
				"videoDetails": map[string]any{"lengthSeconds": "165", "title": "t"},
				"playerConfig": map[string]any{"mediaCommonConfig": map[string]any{"mediaUstreamerRequestConfig": map[string]any{"videoPlaybackUstreamerConfig": "AQID"}}},
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestOAuthDevicePlaybackFlow runs a device-code session end to end against fakes and the test database
// (TEST_DATABASE_URL): an expired access token is renewed once and stored encrypted, the TV player gets the renewed
// Bearer, n is deciphered into the SABR URL, a revoked token marks the session for reconnection, and so does a
// refresh token the token service refuses.
func TestOAuthDevicePlaybackFlow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	t.Setenv("YOUTUBE_SESSION_ENCRYPTION_KEY", "test-only-oauth-flow-key-0123456789abcdef")
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var refreshes atomic.Int32
	var bearer atomic.Value
	fake := fakeTV(t, &refreshes, &bearer)
	defer fake.Close()
	base, _ := url.Parse(fake.URL)
	s := &Server{Cfg: &config.Config{YoutubeTokenURL: fake.URL}, DB: pool,
		YT:   youtube.NewClientWithHTTP(&http.Client{Transport: toFake{base}, Timeout: 10 * time.Second}),
		Sabr: sabr.NewStore(sabr.Options{TokenURL: fake.URL, MaxSessions: 1, Idle: time.Minute, BudgetBytes: 64 << 20})}
	c := s.youtubeCrypto()
	user := "oauth-flow-test-" + contentPlaybackNonce()
	insert := func(access, refresh string, expires int64) {
		now := time.Now().UnixMilli()
		if _, err := pool.Exec(ctx, `INSERT INTO youtube_sessions (user_id, encrypted_cookies, encrypted_po_token, status, created_at, updated_at,
auth_type, encrypted_access_token, encrypted_refresh_token, access_expires_at) VALUES ($1, '', '', 'connected', $2, $2, 'oauth_device', $3, $4, $5)
ON CONFLICT (user_id) DO UPDATE SET status = 'connected', encrypted_access_token = $3, encrypted_refresh_token = $4, access_expires_at = $5`,
			user, now, c.encrypt(access), c.encrypt(refresh), expires); err != nil {
			t.Fatal(err)
		}
	}
	status := func() (st string, access string, expires, used int64) {
		var enc string
		pool.QueryRow(ctx, "SELECT status, encrypted_access_token, access_expires_at, last_used_at FROM youtube_sessions WHERE user_id = $1", user).
			Scan(&st, &enc, &expires, &used)
		access, _ = c.decrypt(enc)
		return
	}
	t.Cleanup(func() { pool.Exec(ctx, "DELETE FROM youtube_sessions WHERE user_id = $1", user) })

	insert("access-1", "refresh-1", time.Now().Add(-time.Minute).UnixMilli())
	access, ok := s.oauthAccess(ctx, user)
	if !ok || access != "access-2" || refreshes.Load() != 1 {
		t.Fatalf("refresh: %q %v after %d calls", access, ok, refreshes.Load())
	}
	if _, stored, exp, _ := status(); stored != "access-2" || exp < time.Now().Add(50*time.Minute).UnixMilli() {
		t.Fatalf("stored %q until %d", stored, exp)
	}
	if again, _ := s.oauthAccess(ctx, user); again != "access-2" || refreshes.Load() != 1 {
		t.Fatalf("a fresh token was refreshed again: %d calls", refreshes.Load())
	}
	info, _, err := s.fetchOAuthSabr(ctx, user, access, "dQw4w9WgXcQ")
	if err != nil {
		t.Fatal(err)
	}
	if bearer.Load() != "Bearer access-2" || info.ClientID != youtube.TVID || !strings.Contains(info.BaseURL, "n=deciphered") ||
		string(info.PoToken) != "pot" || info.VisitorData != "vis" || len(info.Formats) != 1 {
		t.Fatalf("player saw %v, info %+v", bearer.Load(), info)
	}
	if _, _, _, used := status(); used == 0 {
		t.Fatal("last use not recorded")
	}

	if _, _, err := s.fetchOAuthSabr(ctx, user, "revoked", "dQw4w9WgXcQ"); !errors.Is(err, errSessionRejected) {
		t.Fatalf("revoked token: %v", err)
	}
	if st, _, _, _ := status(); st != "needs_reconnect" {
		t.Fatalf("revoked token left status %q", st)
	}

	insert("access-1", "refresh-revoked", time.Now().Add(-time.Minute).UnixMilli())
	if _, ok := s.oauthAccess(ctx, user); ok {
		t.Fatal("a refused refresh token gave access")
	}
	if st, _, _, _ := status(); st != "needs_reconnect" {
		t.Fatalf("refused refresh left status %q", st)
	}
}
