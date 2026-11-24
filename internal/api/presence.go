package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

// Presence keys are PresenceKeyService's read-only "ttp1_" tokens that let a widget or a bot ask what a user is
// playing; only their SHA-256 is stored.
func presenceHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type presenceKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TokenPrefix string `json:"tokenPrefix"`
	Scope       string `json:"scope"`
	CreatedAt   int64  `json:"createdAt"`
	LastUsedAt  *int64 `json:"lastUsedAt"`
}

func (s *Server) presenceRoutes(rt *Router) {
	rt.Handle("POST", "/presence/keys", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if strings.HasPrefix(user, "guest:") {
			plain(w, r, http.StatusForbidden, "Guests cannot create presence keys")
			return
		}
		var req struct {
			Name *string `json:"name"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		name := ""
		if req.Name != nil {
			name = strings.TrimSpace(*req.Name)
		}
		if name == "" || len([]rune(name)) > 50 {
			plain(w, r, http.StatusBadRequest, "Presence key name must contain 1 to 50 characters")
			return
		}
		var count int
		s.DB.QueryRow(r.Context(), "SELECT count(*) FROM presence_keys WHERE user_id = $1", user).Scan(&count)
		if count >= 10 {
			httpx.Error(w, r, http.StatusConflict, "Presence key limit reached", "presence_key_limit")
			return
		}
		raw := make([]byte, 32)
		rand.Read(raw)
		token := "ttp1_" + base64.RawURLEncoding.EncodeToString(raw)
		k := presenceKey{ID: auth.UUID(), Name: name, TokenPrefix: token[:11], Scope: "presence:read", CreatedAt: time.Now().UnixMilli()}
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO presence_keys (id, user_id, name, token_prefix, token_hash, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			k.ID, user, k.Name, k.TokenPrefix, presenceHash(token), k.CreatedAt); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"key": k, "token": token})
	}))
	rt.Handle("GET", "/presence/keys", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if strings.HasPrefix(user, "guest:") {
			plain(w, r, http.StatusForbidden, "Guests cannot own presence keys")
			return
		}
		rows, err := s.DB.Query(r.Context(), "SELECT id, name, token_prefix, created_at, last_used_at FROM presence_keys WHERE user_id = $1 ORDER BY created_at", user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		defer rows.Close()
		out := []presenceKey{}
		for rows.Next() {
			k := presenceKey{Scope: "presence:read"}
			if rows.Scan(&k.ID, &k.Name, &k.TokenPrefix, &k.CreatedAt, &k.LastUsedAt) == nil {
				out = append(out, k)
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("DELETE", "/presence/keys/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		tag, err := s.DB.Exec(r.Context(), "DELETE FROM presence_keys WHERE id = $1 AND user_id = $2", r.PathValue("id"), user)
		if err != nil || tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "Presence key not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("GET", "/presence/now-playing", func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		token = strings.TrimSpace(token)
		var user string
		now := time.Now().UnixMilli()
		if !ok || !strings.HasPrefix(token, "ttp1_") ||
			s.DB.QueryRow(r.Context(), "UPDATE presence_keys SET last_used_at = $2 WHERE token_hash = $1 RETURNING user_id", presenceHash(token), now).Scan(&user) != nil {
			plain(w, r, http.StatusUnauthorized, "Invalid presence token")
			return
		}
		s.activity.mu.Lock()
		np, playing := s.activity.presence[user]
		s.activity.mu.Unlock()
		var current *nowPlaying
		if playing && now-np.UpdatedAt <= 120_000 {
			current = &np
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.JSON(w, http.StatusOK, map[string]any{"active": current != nil, "nowPlaying": current, "retryAfterMs": 15000, "serverTimeMs": now})
	})
}
