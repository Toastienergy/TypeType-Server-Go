package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// JWTs are HS256, signed with JWT_SECRET exactly like java-jwt does it, so tokens issued by the Kotlin server stay
// valid after the switch and the other way round.
type Claims struct {
	ID        string `json:"jti,omitempty"`
	Subject   string `json:"sub,omitempty"`
	SessionID string `json:"sid,omitempty"`
	Expires   int64  `json:"exp,omitempty"`
}

var b64 = base64.RawURLEncoding

const jwtHeader = `{"alg":"HS256","typ":"JWT"}`

var encodedHeader = b64.EncodeToString([]byte(jwtHeader))

var errToken = errors.New("invalid token")

func Sign(secret []byte, c Claims) string {
	payload, _ := json.Marshal(c)
	unsigned := encodedHeader + "." + b64.EncodeToString(payload)
	return unsigned + "." + b64.EncodeToString(mac(secret, unsigned))
}

func mac(secret []byte, s string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(s))
	return h.Sum(nil)
}

type header struct {
	Alg string `json:"alg"`
}

// Verify checks the algorithm, the signature and the time claims (exp, nbf, iat), with no leeway, like
// JWT.require(HMAC256).build().verify().
func Verify(secret []byte, token string, now time.Time) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, errToken
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return c, errToken
	}
	var h header
	if json.Unmarshal(hb, &h) != nil || h.Alg != "HS256" {
		return c, errToken
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac(secret, parts[0]+"."+parts[1])) {
		return c, errToken
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return c, errToken
	}
	var raw struct {
		Claims
		NotBefore *int64 `json:"nbf"`
		IssuedAt  *int64 `json:"iat"`
	}
	if json.Unmarshal(pb, &raw) != nil {
		return c, errToken
	}
	sec := now.Unix()
	if raw.Expires != 0 && sec > raw.Expires {
		return c, errToken
	}
	if raw.NotBefore != nil && sec < *raw.NotBefore {
		return c, errToken
	}
	if raw.IssuedAt != nil && sec < *raw.IssuedAt {
		return c, errToken
	}
	return raw.Claims, nil
}

// SignAny signs any JSON claims set with HS256, for tokens that are not sessions (the OIDC state).
func SignAny(secret []byte, claims any) string {
	payload, _ := json.Marshal(claims)
	unsigned := encodedHeader + "." + b64.EncodeToString(payload)
	return unsigned + "." + b64.EncodeToString(mac(secret, unsigned))
}

// VerifyPayload runs Verify and answers the raw payload, for callers that read their own claims.
func VerifyPayload(secret []byte, token string, now time.Time) ([]byte, error) {
	if _, err := Verify(secret, token, now); err != nil {
		return nil, err
	}
	return b64.DecodeString(strings.Split(token, ".")[1])
}
