package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var usernameFormat = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func guestRefused(w http.ResponseWriter, r *http.Request, user, msg string) bool {
	if strings.HasPrefix(user, "guest:") {
		plain(w, r, http.StatusForbidden, msg)
		return true
	}
	return false
}

func (s *Server) identityUser(ctx context.Context, user string) string {
	if owner, err := s.Auth.Profiles.OwnerID(ctx, user); err == nil && owner != "" {
		return owner
	}
	return user
}

// updateIdentity is AccountIdentityService.update: lower-cased email unique case-insensitively, a changed email
// drops the verified flag.
func (s *Server) updateIdentity(w http.ResponseWriter, r *http.Request, id, email, name string) {
	e, n := strings.ToLower(strings.TrimSpace(email)), strings.TrimSpace(name)
	if l := auth.JavaLength(e); l < 3 || l > 254 || !emailRe.MatchString(e) || auth.JavaLength(n) < 1 || auth.JavaLength(n) > 80 {
		plain(w, r, http.StatusBadRequest, "IDENTITY_INVALID")
		return
	}
	ctx := r.Context()
	var current string
	if err := s.DB.QueryRow(ctx, "SELECT email FROM users WHERE id = $1", id).Scan(&current); err != nil {
		if isNoRows(err) {
			plain(w, r, http.StatusNotFound, "User not found")
			return
		}
		s.fail(w, r, err)
		return
	}
	var taken bool
	s.DB.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = $1 AND id <> $2)", e, id).Scan(&taken)
	if taken {
		plain(w, r, http.StatusConflict, "EMAIL_TAKEN")
		return
	}
	_, err := s.DB.Exec(ctx, `UPDATE users SET email = $2, name = $3, updated_at = $4,
verified = CASE WHEN lower(email) = lower($2) THEN verified ELSE FALSE END WHERE id = $1`, id, e, n, time.Now().UnixMilli())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) profileAccountRoutes(rt *Router) {
	rt.Handle("GET", "/profile/account", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if guestRefused(w, r, user, "Guest users do not have an account") {
			return
		}
		var email, name string
		var issuer *string
		err := s.DB.QueryRow(r.Context(), "SELECT email, name, oidc_issuer FROM users WHERE id = $1", s.identityUser(r.Context(), user)).Scan(&email, &name, &issuer)
		if isNoRows(err) {
			plain(w, r, http.StatusNotFound, "User not found")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"email": email, "name": name, "managedByOidc": issuer != nil})
	}))
	rt.Handle("PUT", "/profile/account", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if guestRefused(w, r, user, "Guest users do not have an account") {
			return
		}
		var req struct{ Email, Name, CurrentPassword *string }
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.Email == nil || req.Name == nil || req.CurrentPassword == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		e := strings.ToLower(strings.TrimSpace(*req.Email))
		if l := auth.JavaLength(e); l < 3 || l > 254 || !emailRe.MatchString(e) || auth.JavaLength(strings.TrimSpace(*req.Name)) < 1 ||
			auth.JavaLength(strings.TrimSpace(*req.Name)) > 80 {
			plain(w, r, http.StatusBadRequest, "IDENTITY_INVALID")
			return
		}
		id := s.identityUser(r.Context(), user)
		var hash string
		var issuer *string
		err := s.DB.QueryRow(r.Context(), "SELECT password_hash, oidc_issuer FROM users WHERE id = $1", id).Scan(&hash, &issuer)
		if isNoRows(err) {
			plain(w, r, http.StatusNotFound, "User not found")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if issuer != nil {
			plain(w, r, http.StatusConflict, "IDENTITY_PROVIDER_MANAGED")
			return
		}
		if !auth.CheckPassword(r.Context(), *req.CurrentPassword, hash) {
			plain(w, r, http.StatusUnauthorized, "CURRENT_PASSWORD_INVALID")
			return
		}
		s.updateIdentity(w, r, id, *req.Email, *req.Name)
	}))
	rt.Handle("PUT", "/admin/users/{id}/identity", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		var req struct{ Email, Name *string }
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.Email == nil || req.Name == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		s.updateIdentity(w, r, r.PathValue("id"), *req.Email, *req.Name)
	}))
	rt.Handle("PUT", "/profile", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if guestRefused(w, r, user, "Guest users cannot update profile") {
			return
		}
		var req struct {
			PublicUsername *string `json:"publicUsername"`
			Bio            *string `json:"bio"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if u := req.PublicUsername; u != nil {
			if n := auth.JavaLength(*u); n < 3 || n > 32 {
				plain(w, r, http.StatusBadRequest, "USERNAME_INVALID_LENGTH")
				return
			}
			if !usernameFormat.MatchString(*u) {
				plain(w, r, http.StatusBadRequest, "USERNAME_INVALID_FORMAT")
				return
			}
		}
		if req.Bio != nil && auth.JavaLength(*req.Bio) > 280 {
			plain(w, r, http.StatusBadRequest, "BIO_TOO_LONG")
			return
		}
		if req.PublicUsername != nil {
			var taken bool
			s.DB.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM users WHERE public_username = $1 AND id <> $2)", *req.PublicUsername, user).Scan(&taken)
			if taken {
				plain(w, r, http.StatusConflict, "USERNAME_TAKEN")
				return
			}
		}
		tag, err := s.DB.Exec(r.Context(), "UPDATE users SET public_username = $2, bio = $3, updated_at = $4 WHERE id = $1", user, req.PublicUsername, req.Bio, time.Now().UnixMilli())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "User not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("GET", "/profile/public/{publicUsername}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("publicUsername")
		var p struct {
			PublicUsername string  `json:"publicUsername"`
			Bio            *string `json:"bio"`
			AvatarURL      *string `json:"avatarUrl"`
			AvatarType     *string `json:"avatarType"`
			AvatarCode     *string `json:"avatarCode"`
		}
		var stored *string
		err := s.DB.QueryRow(r.Context(), "SELECT public_username, bio, avatar_url, avatar_type, avatar_code FROM users WHERE public_username = $1", name).
			Scan(&stored, &p.Bio, &p.AvatarURL, &p.AvatarType, &p.AvatarCode)
		if isNoRows(err) {
			plain(w, r, http.StatusNotFound, "Profile not found")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		p.PublicUsername = name
		if stored != nil {
			p.PublicUsername = *stored
		}
		httpx.JSON(w, http.StatusOK, p)
	})
	rt.Handle("PUT", "/profile/avatar/emoji", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if guestRefused(w, r, user, "Guest users cannot change avatar") {
			return
		}
		var req struct {
			Code *string `json:"code"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		code := ""
		if req.Code != nil {
			code = normalizeEmojiCode(*req.Code)
		}
		if code == "" {
			plain(w, r, http.StatusBadRequest, "Invalid OpenMoji code")
			return
		}
		tag, err := s.DB.Exec(r.Context(), `UPDATE users SET avatar_type = 'emoji', avatar_code = $2, avatar_url = $3, updated_at = $4 WHERE id = $1`,
			user, code, "/avatar/openmoji/"+code+".svg", time.Now().UnixMilli())
		if err != nil || tag.RowsAffected() == 0 {
			plain(w, r, http.StatusBadRequest, "Invalid OpenMoji code")
			return
		}
		s.DB.Exec(r.Context(), "DELETE FROM user_avatars WHERE user_id = $1", user)
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("DELETE", "/profile/avatar", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if guestRefused(w, r, user, "Guest users cannot change avatar") {
			return
		}
		tag, err := s.DB.Exec(r.Context(), "UPDATE users SET avatar_type = NULL, avatar_code = NULL, avatar_url = NULL, updated_at = $2 WHERE id = $1", user, time.Now().UnixMilli())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "User not found")
			return
		}
		s.DB.Exec(r.Context(), "DELETE FROM user_avatars WHERE user_id = $1", user)
		w.WriteHeader(http.StatusNoContent)
	}))
}
