package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"typetype.dev/server/internal/sabr"
	"typetype.dev/server/internal/youtube"
)

// oauthAccess is YoutubeSessionService.oauthAccessToken: the access token of a device-code session, renewed through
// the token service a minute before it expires; a refresh the provider refuses marks the session for reconnection.
func (s *Server) oauthAccess(ctx context.Context, user string) (string, bool) {
	c := s.youtubeCrypto()
	if c == nil || user == "" || strings.HasPrefix(user, "guest:") {
		return "", false
	}
	var encAccess, encRefresh string
	var expires int64
	if s.DB.QueryRow(ctx, `SELECT encrypted_access_token, encrypted_refresh_token, access_expires_at FROM youtube_sessions
WHERE user_id = $1 AND status = 'connected' AND auth_type = 'oauth_device'`, user).Scan(&encAccess, &encRefresh, &expires) != nil {
		return "", false
	}
	access, err := c.decrypt(encAccess)
	if err != nil {
		return "", false
	}
	if expires > time.Now().Add(time.Minute).UnixMilli() {
		return access, true
	}
	refresh, err := c.decrypt(encRefresh)
	if err != nil {
		return "", false
	}
	out := s.tokenCall(ctx, "/device/refresh", map[string]any{"refreshToken": refresh})
	token, _ := out["accessToken"].(string)
	if token == "" {
		s.youtubeNeedsReconnect(ctx, user)
		return "", false
	}
	in, _ := out["expiresIn"].(float64)
	if in <= 0 {
		in = 3600
	}
	s.DB.Exec(ctx, "UPDATE youtube_sessions SET encrypted_access_token = $2, access_expires_at = $3, updated_at = $4 WHERE user_id = $1",
		user, c.encrypt(token), time.Now().Add(time.Duration(in)*time.Second).UnixMilli(), time.Now().UnixMilli())
	return token, true
}

// tvSignature makes the TV player's signature timestamp the eight digits the TV /player wants.
func tvSignature(sts int64) int64 {
	if sts > 0 && sts < 100000 {
		return sts*1000 + 1
	}
	return sts
}

func contentPlaybackNonce() string {
	b := make([]byte, 12)
	rand.Read(b)
	return strings.NewReplacer("+", "-", "/", "_").Replace(base64.StdEncoding.EncodeToString(b))[:16]
}

// fetchOAuthSabr is AuthenticatedSabrOauthInfoService.fetch: the TV /player with the TV PO token, its n parameter
// deciphered by the TV player, as a TVHTML5 SABR session.
func (s *Server) fetchOAuthSabr(ctx context.Context, user, access, videoID string) (*sabr.Info, map[string]any, error) {
	info, player, err := s.tvSabr(ctx, access, videoID)
	if errors.Is(err, errSessionRejected) {
		s.youtubeNeedsReconnect(context.WithoutCancel(ctx), user)
	}
	if err != nil {
		return nil, player, err
	}
	s.DB.Exec(context.WithoutCancel(ctx), "UPDATE youtube_sessions SET last_used_at = $2 WHERE user_id = $1", user, time.Now().UnixMilli())
	return info, player, nil
}

// tvSabr asks the TVHTML5 player for a video, with the viewer's OAuth access token when there is one, and turns its
// answer into SABR session info: the signature timestamp, n and the living-room PO token all come from the token
// service, as for the TV app.
func (s *Server) tvSabr(ctx context.Context, access, videoID string) (*sabr.Info, map[string]any, error) {
	tok := s.Sabr.Token()
	tv, err := tok.TV(ctx)
	if err != nil {
		return nil, nil, err
	}
	dec, err := tok.Decode(ctx, "TVHTML5", nil)
	if err != nil {
		return nil, nil, err
	}
	player, err := s.YT.TVPlayer(ctx, access, videoID, tv.VisitorData, tv.LivingRoomID, tv.PoToken, contentPlaybackNonce(), tvSignature(dec.SignatureTimestamp))
	if err != nil {
		return nil, nil, err
	}
	if access != "" && youtube.Str(player, "playabilityStatus", "status") == "LOGIN_REQUIRED" {
		return nil, nil, errSessionRejected
	}
	if rs := youtube.CheckPlayability(youtube.Obj(player, "playabilityStatus")); rs != nil {
		return nil, player, errors.New(rs.Code + ": " + rs.Message)
	}
	stream, err := s.decipherN(ctx, youtube.Str(player, "streamingData", "serverAbrStreamingUrl"), "TVHTML5")
	if err != nil {
		return nil, player, err
	}
	visitor := youtube.Str(player, "responseContext", "visitorData")
	if visitor == "" {
		visitor = tv.VisitorData
	}
	info, err := sabr.InfoFromPlayer(videoID, player, stream, tv.PoToken, visitor, youtube.TVID, youtube.TVVersion, youtube.TVUA)
	if err != nil {
		return nil, player, err
	}
	return info, player, nil
}
