package api

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

type oidcConfig struct {
	issuer, clientID, clientSecret, discoveryURL, scopes, providerName string
}

// loadOidcConfig is OidcConfigLoader.fromEnvironment: no issuer, client id or secret means OIDC is off.
func loadOidcConfig() *oidcConfig {
	issuer := os.Getenv("OIDC_ISSUER")
	if strings.TrimSpace(issuer) == "" {
		issuer = os.Getenv("OIDC_ISSUER_URL")
	}
	issuer = strings.TrimRight(issuer, "/")
	clientID, secret := strings.TrimSpace(os.Getenv("OIDC_CLIENT_ID")), os.Getenv("OIDC_CLIENT_SECRET")
	if strings.TrimSpace(issuer) == "" || clientID == "" || strings.TrimSpace(secret) == "" {
		return nil
	}
	or := func(name, def string) string {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
		return def
	}
	return &oidcConfig{issuer, clientID, secret, or("OIDC_DISCOVERY_URL", issuer+"/.well-known/openid-configuration"), or("OIDC_SCOPES", "openid email profile"),
		or("OIDC_PROVIDER_NAME", "OIDC")}
}

type oidcMetadata struct{ issuer, authorization, token, jwks string }

var oidcHTTP = &http.Client{Timeout: 30 * time.Second}

func oidcJSON(ctx context.Context, req *http.Request) (map[string]any, error) {
	res, err := oidcHTTP.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		return nil, errors.New("OIDC provider request failed")
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, errors.New("OIDC provider answered invalid JSON")
	}
	return out, nil
}

func jsonString(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", errors.New(`JSONObject["` + key + `"] not found.`)
	}
	s, ok := v.(string)
	if !ok {
		return "", errors.New(`JSONObject["` + key + `"] is not a string.`)
	}
	return s, nil
}

func oidcDiscover(ctx context.Context, cfg *oidcConfig) (oidcMetadata, error) {
	req, _ := http.NewRequest("GET", cfg.discoveryURL, nil)
	if req == nil {
		return oidcMetadata{}, errors.New("OIDC discovery URL is invalid")
	}
	m, err := oidcJSON(ctx, req)
	if err != nil {
		return oidcMetadata{}, err
	}
	var md oidcMetadata
	for _, f := range []struct {
		key string
		dst *string
	}{{"issuer", &md.issuer}, {"authorization_endpoint", &md.authorization}, {"token_endpoint", &md.token}, {"jwks_uri", &md.jwks}} {
		if *f.dst, err = jsonString(m, f.key); err != nil {
			return md, err
		}
	}
	return md, nil
}

// oidcReturnTo keeps an in-app path. The Kotlin server also let "//host" through, a protocol-relative URL that sends
// the browser to another site after sign-in; it is refused here.
func oidcReturnTo(v string) string {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") {
		return "/"
	}
	return v
}

type oidcState struct {
	JTI         string `json:"jti"`
	Nonce       string `json:"nonce"`
	RedirectURI string `json:"redirectUri"`
	ReturnTo    string `json:"returnTo"`
	Exp         int64  `json:"exp"`
}

func (s *Server) oidcRoutes(rt *Router) {
	cfg := loadOidcConfig()
	secret := []byte(s.Cfg.JWTSecret)
	rt.Handle("GET", "/auth/oidc/status", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Admin.Get(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		var name *string
		if cfg != nil {
			name = &cfg.providerName
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"enabled": cfg != nil, "providerName": name, "localLoginEnabled": st.LocalLoginEnabled,
			"autoRedirect": cfg != nil && st.OidcAutoRedirect})
	})
	rt.Handle("GET", "/auth/oidc/start", func(w http.ResponseWriter, r *http.Request) {
		if cfg == nil {
			plain(w, r, http.StatusBadRequest, "OIDC is disabled")
			return
		}
		redirect := strings.TrimSpace(r.URL.Query().Get("redirectUri"))
		if redirect == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'redirectUri' parameter")
			return
		}
		md, err := oidcDiscover(r.Context(), cfg)
		if err != nil {
			plain(w, r, http.StatusUnprocessableEntity, err.Error())
			return
		}
		nonce := auth.UUID()
		state := auth.SignAny(secret, oidcState{JTI: auth.UUID(), Nonce: nonce, RedirectURI: redirect, ReturnTo: oidcReturnTo(r.URL.Query().Get("returnTo")),
			Exp: time.Now().Add(10 * time.Minute).Unix()})
		sep := "?"
		if strings.Contains(md.authorization, "?") {
			sep = "&"
		}
		var q []string
		for _, kv := range [][2]string{{"client_id", cfg.clientID}, {"redirect_uri", redirect}, {"response_type", "code"}, {"scope", cfg.scopes}, {"state", state}, {"nonce", nonce}} {
			q = append(q, javaURLEncode(kv[0])+"="+javaURLEncode(kv[1]))
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"authorizationUrl": md.authorization + sep + strings.Join(q, "&")})
	})
	rt.Handle("POST", "/auth/oidc/callback", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code        *string `json:"code"`
			State       *string `json:"state"`
			RedirectURI *string `json:"redirectUri"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req) != nil || req.Code == nil || req.State == nil || req.RedirectURI == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		if cfg == nil {
			plain(w, r, http.StatusBadRequest, "OIDC is disabled")
			return
		}
		tokens, returnTo, err := s.oidcCallback(r.Context(), cfg, secret, *req.Code, *req.State, *req.RedirectURI)
		if err != nil {
			plain(w, r, http.StatusBadRequest, err.Error())
			return
		}
		s.setRefreshCookie(w, tokens.Refresh, false)
		httpx.JSON(w, http.StatusOK, map[string]string{"accessToken": tokens.Access, "returnTo": returnTo})
	})
}

func (s *Server) oidcCallback(ctx context.Context, cfg *oidcConfig, secret []byte, code, rawState, redirect string) (auth.Tokens, string, error) {
	payload, err := auth.VerifyPayload(secret, rawState, time.Now())
	var st oidcState
	if err != nil || json.Unmarshal(payload, &st) != nil || st.Nonce == "" || st.RedirectURI == "" || st.ReturnTo == "" {
		return auth.Tokens{}, "", errors.New("Invalid OIDC state")
	}
	if st.RedirectURI != redirect {
		return auth.Tokens{}, "", errors.New("OIDC redirect URI mismatch")
	}
	md, err := oidcDiscover(ctx, cfg)
	if err != nil {
		return auth.Tokens{}, "", err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {cfg.clientID}, "client_secret": {cfg.clientSecret}}
	req, err := http.NewRequest("POST", md.token, strings.NewReader(form.Encode()))
	if err != nil {
		return auth.Tokens{}, "", errors.New("OIDC token exchange failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tok, err := oidcJSON(ctx, req)
	if err != nil {
		return auth.Tokens{}, "", errors.New("OIDC token exchange failed")
	}
	idToken, err := jsonString(tok, "id_token")
	if err != nil {
		return auth.Tokens{}, "", err
	}
	if strings.TrimSpace(idToken) == "" {
		return auth.Tokens{}, "", errors.New("OIDC token response is missing id_token")
	}
	id, err := s.oidcIdentity(ctx, cfg, md, idToken, st.Nonce)
	if err != nil {
		return auth.Tokens{}, "", err
	}
	user, err := s.oidcUser(ctx, md.issuer, id)
	if err != nil {
		return auth.Tokens{}, "", err
	}
	t, err := s.Auth.IssueForOwner(ctx, user)
	if err != nil {
		return auth.Tokens{}, "", errors.New("Failed to create session")
	}
	return t, st.ReturnTo, nil
}

type oidcIdentity struct{ subject, email, name string }

// oidcIdentity is OidcIdTokenValidator.validate: an RS256 token signed by a key of the provider's JWKS, for this
// client, carrying the nonce of the state, inside its time claims with no leeway, like java-jwt.
func (s *Server) oidcIdentity(ctx context.Context, cfg *oidcConfig, md oidcMetadata, token, nonce string) (oidcIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return oidcIdentity{}, errors.New("The token was expected to have 3 parts")
	}
	var head struct {
		Alg string  `json:"alg"`
		Kid *string `json:"kid"`
	}
	hb, err1 := decodeB64URL(parts[0])
	pb, err2 := decodeB64URL(parts[1])
	sig, err3 := decodeB64URL(parts[2])
	var claims map[string]any
	if err1 != nil || err2 != nil || err3 != nil || json.Unmarshal(hb, &head) != nil || json.Unmarshal(pb, &claims) != nil {
		return oidcIdentity{}, errors.New("The string doesn't have a valid JSON format")
	}
	if head.Alg != "RS256" {
		return oidcIdentity{}, errors.New("OIDC ID token algorithm is unsupported")
	}
	req, _ := http.NewRequest("GET", md.jwks, nil)
	if req == nil {
		return oidcIdentity{}, errors.New("OIDC provider request failed")
	}
	jwks, err := oidcJSON(ctx, req)
	if err != nil {
		return oidcIdentity{}, err
	}
	key, err := oidcKey(jwks, head.Kid)
	if err != nil {
		return oidcIdentity{}, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return oidcIdentity{}, errors.New("The Token's Signature resulted invalid when verified using the Algorithm: SHA256withRSA")
	}
	str := func(k string) string { v, _ := claims[k].(string); return v }
	num := func(k string) (int64, bool) { v, ok := claims[k].(float64); return int64(v), ok }
	now := time.Now().Unix()
	if exp, ok := num("exp"); ok && now > exp {
		return oidcIdentity{}, errors.New("The Token has expired")
	}
	if nbf, ok := num("nbf"); ok && now < nbf {
		return oidcIdentity{}, errors.New("The Token can't be used before its nbf time")
	}
	if iat, ok := num("iat"); ok && now < iat {
		return oidcIdentity{}, errors.New("The Token can't be used before its iat time")
	}
	if str("iss") != md.issuer {
		return oidcIdentity{}, errors.New("The Claim 'iss' value doesn't match the required issuer.")
	}
	audOK := str("aud") == cfg.clientID
	if list, ok := claims["aud"].([]any); ok {
		for _, a := range list {
			audOK = audOK || a == cfg.clientID
		}
	}
	if !audOK {
		return oidcIdentity{}, errors.New("The Claim 'aud' value doesn't contain the required audience.")
	}
	if str("nonce") != nonce {
		return oidcIdentity{}, errors.New("The Claim 'nonce' value doesn't match the required one.")
	}
	email := strings.TrimSpace(str("email"))
	if email == "" {
		return oidcIdentity{}, errors.New("OIDC email claim is missing")
	}
	name := strings.TrimSpace(str("name"))
	if name == "" {
		name = strings.TrimSpace(str("preferred_username"))
	}
	if name == "" {
		name, _, _ = strings.Cut(email, "@")
	}
	if str("sub") == "" {
		return oidcIdentity{}, errors.New("OIDC callback failed")
	}
	return oidcIdentity{str("sub"), email, name}, nil
}

// oidcKey is OidcRsaKeyResolver.resolve: the first RSA key with the token's kid, or the first RSA key when the
// token names none.
func oidcKey(jwks map[string]any, kid *string) (*rsa.PublicKey, error) {
	keys, _ := jwks["keys"].([]any)
	for _, k := range keys {
		m, _ := k.(map[string]any)
		kty, _ := m["kty"].(string)
		id, _ := m["kid"].(string)
		if kty != "RSA" || kid != nil && id != *kid {
			continue
		}
		n, _ := m["n"].(string)
		e, _ := m["e"].(string)
		nb, err1 := decodeB64URL(n)
		eb, err2 := decodeB64URL(e)
		if err1 != nil || err2 != nil || len(nb) == 0 || len(eb) == 0 || len(eb) > 4 {
			return nil, errors.New("OIDC signing key is invalid")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}, nil
	}
	return nil, errors.New("OIDC signing key not found")
}

// oidcUser is OidcUserService.resolveUserId: the account linked to this identity, else the account with this email,
// linked now, else a new verified account, an admin when it is the first account of the instance.
func (s *Server) oidcUser(ctx context.Context, issuer string, id oidcIdentity) (string, error) {
	var user string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "SELECT id FROM users WHERE oidc_issuer = $1 AND oidc_subject = $2", issuer, id.subject).Scan(&user)
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		now := time.Now().UnixMilli()
		err = tx.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", id.email).Scan(&user)
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE users SET oidc_issuer = $2, oidc_subject = $3, updated_at = $4 WHERE id = $1", user, issuer, id.subject, now)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		hash, err := auth.HashPassword(ctx, auth.UUID())
		if err != nil {
			return err
		}
		user = auth.UUID()
		_, err = tx.Exec(ctx, `INSERT INTO users (id, email, password_hash, name, role, oidc_issuer, oidc_subject, verified, created_at, updated_at)
VALUES ($1, $2, $3, $4, CASE WHEN EXISTS (SELECT 1 FROM users) THEN 'user' ELSE 'admin' END, $5, $6, true, $7, $7)`, user, id.email, hash, id.name, issuer, id.subject, now)
		return err
	})
	return user, err
}
