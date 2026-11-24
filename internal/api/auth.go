package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

const refreshCookie = "refresh_token"

func (s *Server) authRoutes(rt *Router) {
	rt.Handle("GET", "/auth/register/status", s.registerStatus)
	rt.Handle("POST", "/auth/register", s.register)
	rt.Handle("POST", "/auth/login", s.login)
	rt.Handle("POST", "/auth/refresh", s.refresh)
	rt.Handle("POST", "/auth/logout", s.logout)
	rt.Handle("GET", "/auth/me", s.me)
	rt.Handle("POST", "/auth/guest", s.guest)
}

// fail answers an unexpected error with the Kotlin 500 body and logs it.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "requestId", httpx.RequestID(r.Context()), "path", r.URL.Path, "err", err)
	httpx.Error(w, r, http.StatusInternalServerError, "Internal server error", "internal_error")
}

// plain answers an ErrorResponse built with only a message: its code is the Kotlin default "error".
func plain(w http.ResponseWriter, r *http.Request, status int, msg string) {
	httpx.Error(w, r, status, msg, "error")
}

// decode reads a JSON body like kotlinx with ignoreUnknownKeys; a body that does not parse is a 400.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err == nil {
		err = json.Unmarshal(body, v)
	}
	if err != nil {
		httpx.BadRequest(w, r, "Invalid request body")
		return false
	}
	return true
}

func (s *Server) setRefreshCookie(w http.ResponseWriter, token string, clear bool) {
	cfg := s.Auth.Session
	maxAge := int(cfg.RefreshTTL.Seconds())
	if clear {
		token, maxAge = "", 0
	}
	same := "None"
	secure := "; Secure"
	if cfg.AllowInsecureCookies {
		same, secure = "Lax", ""
	}
	w.Header().Add("Set-Cookie", refreshCookie+"="+token+"; Max-Age="+strconv.Itoa(maxAge)+"; Path=/"+secure+"; HttpOnly; SameSite="+same)
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) registerStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Admin.Get(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hasAdmin, err := s.Auth.HasAdmin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, map[string]bool{"allowRegistration": st.AllowRegistration, "bootstrapAvailable": !hasAdmin,
		"localLoginEnabled": st.LocalLoginEnabled})
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    *string `json:"email"`
		Password *string `json:"password"`
		Name     *string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Email == nil || req.Password == nil || req.Name == nil {
		httpx.BadRequest(w, r, "Invalid request body")
		return
	}
	if strings.TrimSpace(*req.Email) == "" || strings.TrimSpace(*req.Password) == "" || strings.TrimSpace(*req.Name) == "" {
		plain(w, r, http.StatusBadRequest, "Missing fields")
		return
	}
	ctx := r.Context()
	st, err := s.Admin.Get(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !st.LocalLoginEnabled {
		plain(w, r, http.StatusForbidden, "Local registration is disabled")
		return
	}
	hasAdmin, err := s.Auth.HasAdmin(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !st.AllowRegistration && hasAdmin {
		plain(w, r, http.StatusForbidden, "Registration is disabled")
		return
	}
	t, err := s.Auth.Register(ctx, *req.Email, *req.Password, *req.Name)
	if err != nil {
		plain(w, r, http.StatusBadRequest, "Registration failed")
		return
	}
	s.signedIn(w, t)
}

func (s *Server) signedIn(w http.ResponseWriter, t auth.Tokens) {
	s.setRefreshCookie(w, t.Refresh, false)
	httpx.JSON(w, http.StatusOK, map[string]string{"accessToken": t.Access})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.Admin.Get(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !st.LocalLoginEnabled {
		plain(w, r, http.StatusForbidden, "Local login is disabled")
		return
	}
	var req struct {
		Identifier *string `json:"identifier"`
		Email      *string `json:"email"`
		Password   *string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Password == nil {
		httpx.BadRequest(w, r, "Invalid request body")
		return
	}
	ident := ""
	if req.Identifier != nil {
		ident = strings.TrimSpace(*req.Identifier)
	}
	if ident == "" && req.Email != nil {
		ident = strings.TrimSpace(*req.Email)
	}
	t, ok, err := s.Auth.Login(ctx, ident, *req.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		plain(w, r, http.StatusUnauthorized, "Invalid credentials")
		return
	}
	s.signedIn(w, t)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	token := cookieValue(r, refreshCookie)
	if token == "" {
		var req struct {
			Token *string `json:"token"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if json.Unmarshal(body, &req) == nil && req.Token != nil {
			token = *req.Token
		}
	}
	if token == "" {
		plain(w, r, http.StatusUnauthorized, "Invalid token")
		return
	}
	t, ok, err := s.Auth.Refresh(r.Context(), token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		plain(w, r, http.StatusUnauthorized, "Invalid token")
		return
	}
	s.signedIn(w, t)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.Auth.Logout(r.Context(), cookieValue(r, refreshCookie)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.setRefreshCookie(w, "", true)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) guest(w http.ResponseWriter, r *http.Request) {
	st, err := s.Admin.Get(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !st.AllowGuest {
		plain(w, r, http.StatusUnauthorized, "Guest access is disabled")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"token": s.Auth.Guest()})
}

// userID reads the bearer token; on failure it has already answered 401 like withJwtAuth.
func (s *Server) userID(w http.ResponseWriter, r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		plain(w, r, http.StatusUnauthorized, "Missing token")
		return "", false
	}
	id, ok, err := s.Auth.Verify(r.Context(), strings.TrimPrefix(h, "Bearer "))
	if err != nil {
		s.fail(w, r, err)
		return "", false
	}
	if !ok {
		plain(w, r, http.StatusUnauthorized, "Invalid token")
		return "", false
	}
	return id, true
}

// optionalUserID is the caller when a valid bearer token is present, "" otherwise.
func (s *Server) optionalUserID(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	id, _, _ := s.Auth.Verify(r.Context(), strings.TrimPrefix(h, "Bearer "))
	return id
}

type userProfile struct {
	ID             string  `json:"id"`
	Role           *string `json:"role"`
	PublicUsername *string `json:"publicUsername"`
	Bio            *string `json:"bio"`
	AvatarURL      *string `json:"avatarUrl"`
	AvatarType     *string `json:"avatarType"`
	AvatarCode     *string `json:"avatarCode"`
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id, ok := s.userID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	role, err := s.Auth.Role(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := userProfile{ID: id, Role: role}
	if !strings.HasPrefix(id, "guest:") {
		err := s.DB.QueryRow(ctx, "SELECT public_username, bio, avatar_url, avatar_type, avatar_code FROM users WHERE id = $1", id).
			Scan(&out.PublicUsername, &out.Bio, &out.AvatarURL, &out.AvatarType, &out.AvatarCode)
		if err != nil && !isNoRows(err) {
			s.fail(w, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}
