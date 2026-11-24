package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// audioToken is AudioOnlyMediaTokenService's v2 token: dot-joined base64url fields and an HMAC-SHA256 over them,
// keyed by sha256("typetype.audio-only." + JWT secret), valid 15 minutes.
type audioToken struct {
	UserID, VideoURL, Locale, TrackID string
	PreferOriginal                    bool
	Itag                              int
	ExpiresAt                         int64
}

const audioTokenTTL = 900 * time.Second

var b64url = base64.RawURLEncoding

func (s *Server) audioKey() []byte {
	k := sha256.Sum256([]byte("typetype.audio-only." + s.Cfg.JWTSecret))
	return k[:]
}

func (s *Server) audioSign(payload string) string {
	m := hmac.New(sha256.New, s.audioKey())
	m.Write([]byte(payload))
	return b64url.EncodeToString(m.Sum(nil))
}

func (s *Server) createAudioToken(t audioToken) string {
	t.ExpiresAt = time.Now().Add(audioTokenTTL).UnixMilli()
	enc := func(v string) string { return b64url.EncodeToString([]byte(v)) }
	payload := strings.Join([]string{"v2", enc(t.UserID), enc(t.VideoURL), strconv.FormatBool(t.PreferOriginal), enc(t.Locale),
		strconv.Itoa(t.Itag), enc(t.TrackID), strconv.FormatInt(t.ExpiresAt, 10)}, ".")
	return payload + "." + s.audioSign(payload)
}

// verifyAudioToken answers the token, or "Invalid audio-only token" / "Audio-only token expired".
func (s *Server) verifyAudioToken(raw string) (audioToken, string) {
	const invalid = "Invalid audio-only token"
	var t audioToken
	parts := strings.Split(raw, ".")
	if len(raw) > 4096 || len(parts) != 9 || parts[0] != "v2" {
		return t, invalid
	}
	got, err1 := decodeB64URL(parts[8])
	want, err2 := b64url.DecodeString(s.audioSign(strings.Join(parts[:8], ".")))
	if err1 != nil || err2 != nil || !hmac.Equal(got, want) {
		return t, invalid
	}
	exp, err := strconv.ParseInt(parts[7], 10, 64)
	if err != nil {
		return t, invalid
	}
	if exp <= time.Now().UnixMilli() {
		return t, "Audio-only token expired"
	}
	fields := make([]string, 4)
	for i, p := range []int{1, 2, 4, 6} {
		b, err := decodeB64URL(parts[p])
		if err != nil {
			return t, invalid
		}
		fields[i] = string(b)
	}
	prefer, err := strconv.ParseBool(parts[3])
	if err != nil || parts[3] != "true" && parts[3] != "false" {
		return t, invalid
	}
	itag, err := strconv.Atoi(parts[5])
	if err != nil {
		return t, invalid
	}
	return audioToken{UserID: fields[0], VideoURL: fields[1], Locale: fields[2], TrackID: fields[3], PreferOriginal: prefer,
		Itag: itag, ExpiresAt: exp}, ""
}

// decodeB64URL accepts base64url with or without padding, like java.util.Base64's URL decoder.
func decodeB64URL(v string) ([]byte, error) {
	return b64url.DecodeString(strings.TrimRight(v, "="))
}
