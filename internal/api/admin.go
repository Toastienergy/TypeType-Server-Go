package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/store"
)

type adminUser struct {
	ID             string  `json:"id"`
	Email          string  `json:"email"`
	Name           string  `json:"name"`
	Role           string  `json:"role"`
	PublicUsername *string `json:"publicUsername"`
	Bio            *string `json:"bio"`
	AvatarURL      *string `json:"avatarUrl"`
	AvatarType     *string `json:"avatarType"`
	AvatarCode     *string `json:"avatarCode"`
	Suspended      bool    `json:"suspended"`
	Verified       bool    `json:"verified"`
	AccessMode     string  `json:"accessMode"`
	CreatedAt      int64   `json:"createdAt"`
}

const adminUserColumns = `u.id, u.email, u.name, u.role, u.public_username, u.bio, u.avatar_url, u.avatar_type, u.avatar_code, u.suspended,
u.verified, COALESCE(st.access_mode, ''), u.created_at FROM users u LEFT JOIN settings st ON st.user_id = u.id`

func (s *Server) adminUsers(ctx context.Context, where string, args ...any) ([]adminUser, error) {
	rows, err := s.DB.Query(ctx, "SELECT "+adminUserColumns+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adminUser{}
	for rows.Next() {
		var u adminUser
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.PublicUsername, &u.Bio, &u.AvatarURL, &u.AvatarType, &u.AvatarCode, &u.Suspended,
			&u.Verified, &u.AccessMode, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.AccessMode = store.ToAccessMode(u.AccessMode)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Server) adminRoutes(rt *Router) {
	rt.Handle("GET", "/admin/users", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		q := r.URL.Query()
		if !q.Has("page") && !q.Has("limit") {
			users, err := s.adminUsers(r.Context(), "ORDER BY u.created_at DESC")
			if err != nil {
				s.fail(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, users)
			return
		}
		page, limit := 1, 50
		var err error
		if q.Has("page") {
			if page, err = strconv.Atoi(q.Get("page")); err != nil || page < 1 {
				plain(w, r, http.StatusBadRequest, "Invalid page")
				return
			}
		}
		if q.Has("limit") {
			if limit, err = strconv.Atoi(q.Get("limit")); err != nil || limit < 1 || limit > 200 {
				plain(w, r, http.StatusBadRequest, "Invalid limit")
				return
			}
		}
		var total int64
		s.DB.QueryRow(r.Context(), "SELECT count(*) FROM users").Scan(&total)
		users, err := s.adminUsers(r.Context(), "ORDER BY u.created_at DESC LIMIT $1 OFFSET $2", limit, int64(page-1)*int64(limit))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": users, "page": page, "limit": limit, "total": total})
	}))
	suspend := func(on bool) http.HandlerFunc {
		return s.adminOnly(func(w http.ResponseWriter, r *http.Request, admin string) {
			id := r.PathValue("id")
			if id == admin {
				plain(w, r, http.StatusForbidden, "Cannot suspend your own account")
				return
			}
			tag, err := s.DB.Exec(r.Context(), "UPDATE users SET suspended = $2, updated_at = $3 WHERE id = $1", id, on, time.Now().UnixMilli())
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if tag.RowsAffected() == 0 {
				plain(w, r, http.StatusNotFound, "User not found")
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	rt.Handle("POST", "/admin/users/{id}/suspend", suspend(true))
	rt.Handle("DELETE", "/admin/users/{id}/suspend", suspend(false))
	rt.Handle("PUT", "/admin/users/{id}/role", s.adminOnly(func(w http.ResponseWriter, r *http.Request, admin string) {
		id := r.PathValue("id")
		if id == admin {
			plain(w, r, http.StatusForbidden, "Cannot modify your own role")
			return
		}
		var req struct {
			Role *string `json:"role"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.Role == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		ok := *req.Role == "user" || *req.Role == "moderator" || *req.Role == "admin"
		if ok {
			tag, err := s.DB.Exec(r.Context(), "UPDATE users SET role = $2, updated_at = $3 WHERE id = $1", id, *req.Role, time.Now().UnixMilli())
			ok = err == nil && tag.RowsAffected() > 0
		}
		if !ok {
			plain(w, r, http.StatusBadRequest, "Invalid role or user not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("POST", "/admin/users/{id}/reset-token", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		token := auth.UUID()
		if _, err := s.DB.Exec(r.Context(), "INSERT INTO password_resets (id, user_id, token, expires_at) VALUES ($1, $2, $3, $4)",
			auth.UUID(), r.PathValue("id"), token, time.Now().Add(time.Hour).UnixMilli()); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]string{"resetToken": token})
	}))
	rt.Handle("GET", "/admin/settings", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		st, err := s.Admin.Get(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		noStore(w)
		httpx.JSON(w, http.StatusOK, st)
	}))
	rt.Handle("PUT", "/admin/settings", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		cur, err := s.Admin.Get(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if json.Unmarshal(body, &cur) != nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		saved, err := s.Admin.Upsert(r.Context(), cur)
		var inv store.ErrInvalid
		if errors.As(err, &inv) {
			plain(w, r, http.StatusBadRequest, inv.Msg)
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		noStore(w)
		httpx.JSON(w, http.StatusOK, saved)
	}))
	rt.Handle("GET", "/admin/users/search", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			httpx.JSON(w, http.StatusOK, []any{})
			return
		}
		limit := 20
		if r.URL.Query().Has("limit") {
			n, err := strconv.Atoi(r.URL.Query().Get("limit"))
			if err != nil || n < 1 || n > 50 {
				plain(w, r, http.StatusBadRequest, "Invalid limit")
				return
			}
			limit = n
		}
		users, err := s.adminUsers(r.Context(), "WHERE lower(u.email) LIKE $1 OR lower(u.name) LIKE $1 ORDER BY u.created_at DESC LIMIT $2",
			"%"+strings.ToLower(q)+"%", limit)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := make([]map[string]string, len(users))
		for i, u := range users {
			out[i] = map[string]string{"id": u.ID, "email": u.Email, "name": u.Name, "accessMode": u.AccessMode}
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("PUT", "/admin/users/{id}/access-mode", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		var req struct {
			AccessMode *string `json:"accessMode"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.AccessMode == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		if *req.AccessMode != "unrestricted" && *req.AccessMode != "allow_list" {
			plain(w, r, http.StatusBadRequest, "Invalid accessMode")
			return
		}
		id := r.PathValue("id")
		var exists bool
		s.DB.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", id).Scan(&exists)
		if !exists {
			plain(w, r, http.StatusNotFound, "User not found")
			return
		}
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO settings (user_id, access_mode, access_mode_admin_managed, access_mode_admin_managed_at)
VALUES ($1, $2, TRUE, $3) ON CONFLICT (user_id) DO UPDATE SET access_mode = EXCLUDED.access_mode, access_mode_admin_managed = TRUE,
access_mode_admin_managed_at = EXCLUDED.access_mode_admin_managed_at`, id, *req.AccessMode, time.Now().UnixMilli()); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"accessMode": *req.AccessMode})
	}))
	rt.Handle("GET", "/admin/sessions", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		st, err := s.Admin.Get(r.Context())
		if err != nil || !st.ActiveSessionsEnabled {
			httpx.JSON(w, http.StatusOK, []any{})
			return
		}
		s.activity.mu.Lock()
		list := make([]activeSession, 0, len(s.activity.sessions))
		now := time.Now().UnixMilli()
		for k, v := range s.activity.sessions {
			if now-v.LastActivityAt > sessionInactivity.Milliseconds() {
				delete(s.activity.sessions, k)
				continue
			}
			list = append(list, *v)
		}
		s.activity.mu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].LastActivityAt > list[j].LastActivityAt })
		out := make([]map[string]any, len(list))
		for i, a := range list {
			var name *string
			var n string
			if s.DB.QueryRow(r.Context(), "SELECT name FROM users WHERE id = $1", a.UserID).Scan(&n) == nil {
				name = &n
			}
			b, _ := json.Marshal(a)
			m := map[string]any{}
			json.Unmarshal(b, &m)
			m["username"], m["remoteAddress"] = name, nil
			out[i] = m
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("POST", "/auth/reset-password", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ResetToken  *string `json:"resetToken"`
			NewPassword *string `json:"newPassword"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.ResetToken == nil || req.NewPassword == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		if strings.TrimSpace(*req.NewPassword) == "" {
			plain(w, r, http.StatusBadRequest, "Password cannot be blank")
			return
		}
		ctx, now := r.Context(), time.Now().UnixMilli()
		var user string
		var expires int64
		var used *int64
		err := s.DB.QueryRow(ctx, "SELECT user_id, expires_at, used_at FROM password_resets WHERE token = $1", *req.ResetToken).Scan(&user, &expires, &used)
		if err != nil || expires < now || used != nil {
			plain(w, r, http.StatusBadRequest, "Invalid or expired reset token")
			return
		}
		hash, err := auth.HashPassword(ctx, *req.NewPassword)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.DB.Exec(ctx, "UPDATE users SET password_hash = $2, updated_at = $3 WHERE id = $1", user, hash, now)
		s.DB.Exec(ctx, "UPDATE password_resets SET used_at = $2 WHERE token = $1", *req.ResetToken, now)
		w.WriteHeader(http.StatusNoContent)
	})
}
