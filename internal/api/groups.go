package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
)

const maxMembershipChannels = 500

type groupItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ChannelCount int    `json:"channelCount"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

func groupNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotFound, "Subscription group not found", "subscription_group_not_found")
}

func groupName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Name *string `json:"name"`
	}
	if !decodeOr(w, r, &req, "Invalid request body") {
		return "", false
	}
	if req.Name == nil {
		plain(w, r, http.StatusBadRequest, "Invalid request body")
		return "", false
	}
	n := strings.TrimSpace(*req.Name)
	if l := auth.JavaLength(n); l < 1 || l > 100 {
		httpx.Error(w, r, http.StatusBadRequest, "Group name must contain 1 to 100 characters", "subscription_group_invalid_name")
		return "", false
	}
	return n, true
}

// membershipChannels reads {"channelUrl": ...} or {"channelUrls": [...]}, never both.
func membershipChannels(w http.ResponseWriter, r *http.Request, body []byte) ([]string, bool) {
	var req struct {
		ChannelURL  *string  `json:"channelUrl"`
		ChannelURLs []string `json:"channelUrls"`
	}
	valid := func(u string) bool { return strings.TrimSpace(u) != "" && len(u) <= 2048 }
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && json.Unmarshal(body, &req) == nil {
		switch {
		case req.ChannelURL != nil && req.ChannelURLs == nil && valid(*req.ChannelURL):
			return []string{*req.ChannelURL}, true
		case req.ChannelURL == nil && req.ChannelURLs != nil && len(req.ChannelURLs) >= 1 && len(req.ChannelURLs) <= maxMembershipChannels && all(req.ChannelURLs, valid):
			return req.ChannelURLs, true
		}
	}
	plain(w, r, http.StatusBadRequest, "Invalid request body")
	return nil, false
}

func (s *Server) groupExists(r *http.Request, user, id string) bool {
	var ok bool
	s.DB.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM subscription_groups WHERE user_id = $1 AND id = $2)", user, id).Scan(&ok)
	return ok
}

func (s *Server) groupRoutes(rt *Router) {
	rt.Handle("GET", "/subscriptions/groups", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		rows, err := s.DB.Query(r.Context(), `SELECT g.id, g.name, g.created_at, g.updated_at,
(SELECT count(*) FROM subscription_group_memberships m WHERE m.group_id = g.id AND m.user_id = $1)
FROM subscription_groups g WHERE g.user_id = $1 ORDER BY lower(g.name), g.created_at`, user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		defer rows.Close()
		out := []groupItem{}
		for rows.Next() {
			var g groupItem
			if rows.Scan(&g.ID, &g.Name, &g.CreatedAt, &g.UpdatedAt, &g.ChannelCount) == nil {
				out = append(out, g)
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("POST", "/subscriptions/groups", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		name, ok := groupName(w, r)
		if !ok {
			return
		}
		now := time.Now().UnixMilli()
		g := groupItem{ID: auth.UUID(), Name: name, CreatedAt: now, UpdatedAt: now}
		tag, err := s.DB.Exec(r.Context(), `INSERT INTO subscription_groups (id, user_id, name, normalized_name, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5) ON CONFLICT DO NOTHING`, g.ID, user, name, strings.ToLower(name), now)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			httpx.Error(w, r, http.StatusConflict, "A subscription group with this name already exists", "subscription_group_name_conflict")
			return
		}
		httpx.JSON(w, http.StatusCreated, g)
	}))
	rt.Handle("PUT", "/subscriptions/groups/{groupId}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		name, ok := groupName(w, r)
		if !ok {
			return
		}
		id := r.PathValue("groupId")
		if !s.groupExists(r, user, id) {
			groupNotFound(w, r)
			return
		}
		var dup bool
		s.DB.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM subscription_groups WHERE user_id = $1 AND normalized_name = $2 AND id <> $3)",
			user, strings.ToLower(name), id).Scan(&dup)
		if dup {
			httpx.Error(w, r, http.StatusConflict, "A subscription group with this name already exists", "subscription_group_name_conflict")
			return
		}
		if _, err := s.DB.Exec(r.Context(), "UPDATE subscription_groups SET name = $3, normalized_name = $4, updated_at = $5 WHERE user_id = $1 AND id = $2",
			user, id, name, strings.ToLower(name), time.Now().UnixMilli()); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("DELETE", "/subscriptions/groups/{groupId}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		id := r.PathValue("groupId")
		s.DB.Exec(r.Context(), "DELETE FROM subscription_group_memberships WHERE user_id = $1 AND group_id = $2", user, id)
		tag, err := s.DB.Exec(r.Context(), "DELETE FROM subscription_groups WHERE user_id = $1 AND id = $2", user, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			groupNotFound(w, r)
			return
		}
		s.feeds.invalidate(user)
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("PUT", "/subscriptions/groups/{groupId}/channels", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		urls, ok := membershipChannels(w, r, body)
		if !ok {
			return
		}
		s.changeMembership(w, r, user, urls, true)
	}))
	rt.Handle("DELETE", "/subscriptions/groups/{groupId}/channels", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		q := r.URL.Query().Get("url")
		if r.URL.Query().Has("url") {
			if strings.TrimSpace(q) == "" {
				plain(w, r, http.StatusBadRequest, "Invalid channel URL")
				return
			}
			if len(strings.TrimSpace(string(body))) > 0 {
				plain(w, r, http.StatusBadRequest, "Specify either url or a request body, not both")
				return
			}
			s.changeMembership(w, r, user, []string{q}, false)
			return
		}
		urls, ok := membershipChannels(w, r, body)
		if !ok {
			return
		}
		s.changeMembership(w, r, user, urls, false)
	}))
}

func (s *Server) changeMembership(w http.ResponseWriter, r *http.Request, user string, urls []string, add bool) {
	ctx, id := r.Context(), r.PathValue("groupId")
	if !s.groupExists(r, user, id) {
		groupNotFound(w, r)
		return
	}
	now := time.Now().UnixMilli()
	for _, raw := range urls {
		c := db.CanonicalChannelURL(raw)
		if add {
			var subscribed bool
			s.DB.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM subscriptions WHERE user_id = $1 AND channel_url = $2)", user, c).Scan(&subscribed)
			if !subscribed {
				httpx.Error(w, r, http.StatusNotFound, "Subscription not found", "subscription_not_found")
				return
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO subscription_group_memberships (group_id, user_id, channel_url, added_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (group_id, channel_url) DO NOTHING`, id, user, c, now); err != nil {
				s.fail(w, r, err)
				return
			}
			continue
		}
		tag, err := s.DB.Exec(ctx, "DELETE FROM subscription_group_memberships WHERE group_id = $1 AND user_id = $2 AND channel_url = $3", id, user, c)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 && len(urls) == 1 {
			httpx.Error(w, r, http.StatusNotFound, "Subscription group membership not found", "subscription_group_membership_not_found")
			return
		}
	}
	s.DB.Exec(ctx, "UPDATE subscription_groups SET updated_at = $3 WHERE user_id = $1 AND id = $2", user, id, now)
	w.WriteHeader(http.StatusNoContent)
}
