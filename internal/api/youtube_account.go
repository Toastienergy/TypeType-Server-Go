package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/sabr"
	"typetype.dev/server/internal/youtube"
)

var errSessionRejected = errors.New("youtube session needs reconnect")

// youtubeAccount is YoutubeSessionService.connectedCredentials for a cookie session; a session that no longer
// decrypts is marked for reconnection.
func (s *Server) youtubeAccount(ctx context.Context, user string) (*youtube.Account, bool) {
	if user == "" || strings.HasPrefix(user, "guest:") {
		return nil, false
	}
	c := s.youtubeCrypto()
	if c == nil {
		return nil, false
	}
	var enc, kind string
	var authUser int
	if s.DB.QueryRow(ctx, "SELECT encrypted_cookies, auth_user, auth_type FROM youtube_sessions WHERE user_id = $1 AND status = 'connected'",
		user).Scan(&enc, &authUser, &kind) != nil || kind != "cookies" {
		return nil, false
	}
	cookies, err := c.decrypt(enc)
	if err != nil {
		s.youtubeNeedsReconnect(ctx, user)
		return nil, false
	}
	return &youtube.Account{Cookies: cookies, AuthUser: authUser}, true
}

func (s *Server) youtubeNeedsReconnect(ctx context.Context, user string) {
	s.DB.Exec(ctx, "UPDATE youtube_sessions SET status = 'needs_reconnect', updated_at = $2 WHERE user_id = $1", user, time.Now().UnixMilli())
}

// accountPlayback is AuthenticatedSabrInfoService for cookie sessions: the signed-in web /player of a video with
// the session PO token, kept per viewer and video until ten minutes before its URL expires.
type accountPlayback struct {
	mu       sync.Mutex
	visitors map[string]visitorAt
	infos    map[string]accountInfo
	flights  singleflight.Group
}

type visitorAt struct {
	value string
	at    time.Time
}

type accountInfo struct {
	info   *sabr.Info
	player map[string]any
}

var accountPlaybacks = &accountPlayback{visitors: map[string]visitorAt{}, infos: map[string]accountInfo{}}

// requiresSession remembers videos YouTube only plays to a signed-in viewer, as the public stream answer showed.
var requiresSession sync.Map

// hasYoutubeSession tells whether the viewer has a connected YouTube session of either kind.
func (s *Server) hasYoutubeSession(ctx context.Context, user string) bool {
	if user == "" || strings.HasPrefix(user, "guest:") || s.youtubeCrypto() == nil {
		return false
	}
	var n int
	return s.DB.QueryRow(ctx, "SELECT count(*) FROM youtube_sessions WHERE user_id = $1 AND status = 'connected'", user).Scan(&n) == nil && n > 0
}

// signedInSabr is AuthenticatedSabrInfoService.fetch: a cookie session plays through the signed-in web /player, a
// device-code session through the TV /player; the answer is kept per viewer and video until ten minutes before
// its URL expires.
func (s *Server) signedInSabr(ctx context.Context, user, videoID string) (*sabr.Info, map[string]any, error) {
	a := accountPlaybacks
	key := user + "\x00" + videoID
	a.mu.Lock()
	if c, ok := a.infos[key]; ok && time.Until(c.info.Expires) > 10*time.Minute {
		a.mu.Unlock()
		return c.info, c.player, nil
	}
	a.mu.Unlock()
	v, err, _ := a.flights.Do(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		var info *sabr.Info
		var player map[string]any
		var err error
		if acct, ok := s.youtubeAccount(ctx, user); ok {
			info, player, err = s.fetchAccountSabr(ctx, user, acct, videoID)
		} else if access, ok := s.oauthAccess(ctx, user); ok {
			info, player, err = s.fetchOAuthSabr(ctx, user, access, videoID)
		} else {
			err = errors.New("no connected YouTube session")
		}
		if err != nil {
			return nil, err
		}
		c := accountInfo{info, player}
		a.mu.Lock()
		a.infos[key] = c
		a.mu.Unlock()
		return c, nil
	})
	if err != nil {
		return nil, nil, err
	}
	c := v.(accountInfo)
	return c.info, c.player, nil
}

// decipherN replaces the n parameter of a streaming URL with its value deciphered by the token service's decoder.
func (s *Server) decipherN(ctx context.Context, raw, clientName string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return "", errors.New("no SABR URL for the signed-in player")
	}
	n := u.Query().Get("n")
	if n == "" {
		return raw, nil
	}
	d, err := s.Sabr.Token().Decode(ctx, clientName, []string{n})
	if err != nil || d.Throttling[n] == "" {
		return "", errors.New("could not decipher the n parameter")
	}
	q := u.Query()
	q.Set("n", d.Throttling[n])
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Server) accountVisitor(ctx context.Context, user string, acct *youtube.Account) (string, error) {
	a := accountPlaybacks
	a.mu.Lock()
	if v, ok := a.visitors[user]; ok && time.Since(v.at) < 30*time.Minute {
		a.mu.Unlock()
		return v.value, nil
	}
	a.mu.Unlock()
	vd, err := s.YT.AccountVisitorData(ctx, *acct)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.visitors[user] = visitorAt{vd, time.Now()}
	a.mu.Unlock()
	return vd, nil
}

func (s *Server) fetchAccountSabr(ctx context.Context, user string, acct *youtube.Account, videoID string) (*sabr.Info, map[string]any, error) {
	visitor, err := s.accountVisitor(ctx, user, acct)
	if err != nil {
		return nil, nil, err
	}
	tok := s.Sabr.Token()
	tokens, err := tok.SessionTokens(ctx, videoID, visitor)
	if err != nil {
		return nil, nil, err
	}
	dec, err := tok.Decode(ctx, "", nil)
	if err != nil {
		return nil, nil, err
	}
	po := tokens.SessionPoToken
	if po == "" {
		po = tokens.VisitorPoToken
	}
	player, err := s.YT.AccountPlayer(ctx, *acct, videoID, visitor, po, dec.SignatureTimestamp)
	if err != nil {
		return nil, nil, err
	}
	if youtube.Str(player, "playabilityStatus", "status") == "LOGIN_REQUIRED" {
		s.youtubeNeedsReconnect(context.WithoutCancel(ctx), user)
		return nil, nil, errSessionRejected
	}
	if rs := youtube.CheckPlayability(youtube.Obj(player, "playabilityStatus")); rs != nil {
		return nil, player, errors.New(rs.Code + ": " + rs.Message)
	}
	stream, err := s.decipherN(ctx, youtube.Str(player, "streamingData", "serverAbrStreamingUrl"), "")
	if err != nil {
		return nil, player, err
	}
	streaming := tokens.VideoPoToken
	if streaming == "" {
		streaming = tokens.StreamingPot
	}
	info, err := sabr.InfoFromPlayer(videoID, player, stream, streaming, visitor, youtube.Web.ID, youtube.Web.Version, youtube.Web.UA)
	if err != nil {
		return nil, player, err
	}
	s.DB.Exec(context.WithoutCancel(ctx), "UPDATE youtube_sessions SET last_used_at = $2 WHERE user_id = $1", user, time.Now().UnixMilli())
	return info, player, nil
}

// writeAccountStream is YoutubeSessionSabrStreamService: the page of a video YouTube only plays to a signed-in
// viewer, from the account's /player, with the related videos the public page already filtered for the viewer.
func (s *Server) writeAccountStream(w http.ResponseWriter, r *http.Request, user, id, raw string, public *streamResponse) {
	info, player, err := s.signedInSabr(r.Context(), user, id)
	switch {
	case errors.Is(err, errSessionRejected):
		httpx.Error(w, r, http.StatusBadRequest, "YouTube session needs reconnect", "youtube_session_needs_reconnect")
		return
	case err != nil:
		s.Log.Warn("youtube account stream", "videoId", id, "err", err)
		httpx.Error(w, r, http.StatusBadGateway, "Could not extract this video", "extraction_failed")
		return
	}
	out := withPublicMetadata(buildStream(id, raw, youtube.ParsePlayer(id, player), info), public)
	out.RelatedStreams, out.SponsorBlockSegments = public.RelatedStreams, public.SponsorBlockSegments
	httpx.JSON(w, http.StatusOK, out)
}

// needsSignedIn reports a public lookup that failed because YouTube wants a signed-in viewer: the video was already
// seen to need it, or the token service's anonymous player asked for a sign-in.
func needsSignedIn(id string, err error) bool {
	if err == nil {
		return false
	}
	if _, ok := requiresSession.Load(id); ok {
		return true
	}
	if strings.Contains(err.Error(), "LOGIN_REQUIRED") {
		requiresSession.Store(id, true)
		return true
	}
	return false
}
