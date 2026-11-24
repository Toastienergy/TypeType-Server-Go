package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/httpx"
)

func decodeEnabled(r *http.Request) (bool, bool) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req) != nil || req.Enabled == nil {
		return false, false
	}
	return *req.Enabled, true
}

func (s *Server) rssRoutes(rt *Router) {
	rt.Handle("GET", "/rss/feeds", s.rssUser(func(w http.ResponseWriter, r *http.Request, user string) {
		if _, _, err := s.rssAvailable(r.Context(), user); err != nil {
			s.rssFail(w, r, err)
			return
		}
		feeds, err := rssFeeds(r.Context(), s.DB, "WHERE user_id = $1 ORDER BY created_at DESC", user)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if feeds == nil {
			feeds = []*rssFeed{}
		}
		respondNoStore(w, feeds)
	}))
	rt.Handle("POST", "/rss/feeds", s.rssUser(func(w http.ResponseWriter, r *http.Request, user string) {
		req, ok := decodeRssRequest(r)
		if !ok {
			rssBodyError(w, r)
			return
		}
		base, limit, err := s.rssAvailable(r.Context(), user)
		if err == nil {
			req, err = s.normalizeRss(r.Context(), user, req)
		}
		if err != nil {
			s.rssFail(w, r, err)
			return
		}
		secret, hash := rssSecret()
		id := uuid.NewString()
		var feed *rssFeed
		err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			if _, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock($1)", int64(javaHash(user))); err != nil {
				return err
			}
			var count int
			if err := tx.QueryRow(r.Context(), "SELECT count(*) FROM rss_feeds WHERE user_id = $1", user).Scan(&count); err != nil {
				return err
			}
			if count >= limit {
				return rssError{"RSS feed limit reached", "rss_feed_limit_reached"}
			}
			now := time.Now().UnixMilli()
			if _, err := tx.Exec(r.Context(), `INSERT INTO rss_feeds (id, user_id, name, token_hash, scope, include_videos, include_shorts, include_live, include_upcoming,
enabled, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, true, $10, $10)`, id, user, req.Name, hash, req.Scope, req.IncludeVideos, req.IncludeShorts,
				req.IncludeLive, req.IncludeUpcoming, now); err != nil {
				return err
			}
			if err := replaceRssSelections(r.Context(), tx, id, req); err != nil {
				return err
			}
			feed, err = rssFeedByID(r.Context(), tx, id)
			return err
		})
		if err != nil {
			s.rssFail(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.JSON(w, http.StatusCreated, map[string]any{"feed": feed, "feedUrl": rssFeedURL(base, feed.ID, secret)})
	}))
	rt.Handle("PUT", "/rss/feeds/{id}", s.rssUser(func(w http.ResponseWriter, r *http.Request, user string) {
		id := r.PathValue("id")
		req, ok := decodeRssRequest(r)
		if !ok {
			rssBodyError(w, r)
			return
		}
		_, _, err := s.rssAvailable(r.Context(), user)
		if err == nil {
			req, err = s.normalizeRss(r.Context(), user, req)
		}
		if err != nil {
			s.rssFail(w, r, err)
			return
		}
		var feed *rssFeed
		err = pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			tag, err := tx.Exec(r.Context(), `UPDATE rss_feeds SET name = $3, scope = $4, include_videos = $5, include_shorts = $6, include_live = $7, include_upcoming = $8,
updated_at = $9 WHERE id = $1 AND user_id = $2`, id, user, req.Name, req.Scope, req.IncludeVideos, req.IncludeShorts, req.IncludeLive, req.IncludeUpcoming, time.Now().UnixMilli())
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errRssNotFound
			}
			if err := replaceRssSelections(r.Context(), tx, id, req); err != nil {
				return err
			}
			feed, err = rssFeedByID(r.Context(), tx, id)
			return err
		})
		if err != nil {
			s.rssFail(w, r, err)
			return
		}
		respondNoStore(w, feed)
	}))
	rt.Handle("PUT", "/rss/feeds/{id}/enabled", s.rssUser(func(w http.ResponseWriter, r *http.Request, user string) {
		enabled, ok := decodeEnabled(r)
		if !ok {
			rssBodyError(w, r)
			return
		}
		if _, _, err := s.rssAvailable(r.Context(), user); err != nil {
			s.rssFail(w, r, err)
			return
		}
		feed, err := s.rssUpdate(r.Context(), &user, r.PathValue("id"), "enabled = $2, updated_at = $3", enabled, time.Now().UnixMilli())
		if err != nil {
			s.rssFail(w, r, err)
			return
		}
		respondNoStore(w, feed)
	}))
	rt.Handle("POST", "/rss/feeds/{id}/regenerate", s.rssUser(func(w http.ResponseWriter, r *http.Request, user string) {
		base, _, err := s.rssAvailable(r.Context(), user)
		if err != nil {
			s.rssFail(w, r, err)
			return
		}
		secret, hash := rssSecret()
		feed, err := s.rssUpdate(r.Context(), &user, r.PathValue("id"), "token_hash = $2, updated_at = $3", hash, time.Now().UnixMilli())
		if err != nil {
			s.rssFail(w, r, err)
			return
		}
		respondNoStore(w, map[string]any{"feed": feed, "feedUrl": rssFeedURL(base, feed.ID, secret)})
	}))
	rt.Handle("DELETE", "/rss/feeds/{id}", s.rssUser(func(w http.ResponseWriter, r *http.Request, user string) {
		if _, _, err := s.rssAvailable(r.Context(), user); err != nil {
			s.rssFail(w, r, err)
			return
		}
		if err := s.deleteRssFeed(r, r.PathValue("id"), &user); err != nil {
			s.rssFail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	s.rssAdminRoutes(rt)
	rt.Handle("GET", "/rss/feeds/{file}", s.readRss)
}

func (s *Server) deleteRssFeed(r *http.Request, id string, owner *string) error {
	return pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
		var exists bool
		q, args := "SELECT EXISTS (SELECT 1 FROM rss_feeds WHERE id = $1)", []any{id}
		if owner != nil {
			q, args = "SELECT EXISTS (SELECT 1 FROM rss_feeds WHERE id = $1 AND user_id = $2)", []any{id, *owner}
		}
		if err := tx.QueryRow(r.Context(), q, args...).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errRssNotFound
		}
		b := &pgx.Batch{}
		b.Queue("DELETE FROM rss_feed_channels WHERE feed_id = $1", id)
		b.Queue("DELETE FROM rss_feed_services WHERE feed_id = $1", id)
		b.Queue("DELETE FROM rss_feeds WHERE id = $1", id)
		return tx.SendBatch(r.Context(), b).Close()
	})
}

func (s *Server) rssAdminRoutes(rt *Router) {
	rt.Handle("GET", "/admin/rss/feeds", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		page, limit := 1, 50
		q := r.URL.Query()
		var bad bool
		if v := q.Get("page"); q.Has("page") {
			n, err := strconv.Atoi(v)
			page, bad = n, err != nil
		}
		if v := q.Get("limit"); q.Has("limit") && !bad {
			n, err := strconv.Atoi(v)
			limit, bad = n, err != nil
		}
		if bad || page < 1 || limit < 1 || limit > 200 {
			plain(w, r, http.StatusBadRequest, "Invalid pagination")
			return
		}
		var total int64
		if err := s.DB.QueryRow(r.Context(), "SELECT count(*) FROM rss_feeds").Scan(&total); err != nil {
			s.fail(w, r, err)
			return
		}
		feeds, err := rssFeeds(r.Context(), s.DB, "ORDER BY created_at DESC LIMIT $1 OFFSET $2", limit, int64(page-1)*int64(limit))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		items := []map[string]any{}
		for _, f := range feeds {
			var name, email string
			var suspended bool
			rssOn := true
			err := s.DB.QueryRow(r.Context(), "SELECT u.name, u.email, u.suspended, COALESCE(p.enabled, true) FROM users u LEFT JOIN rss_user_policies p ON p.user_id = u.id WHERE u.id = $1",
				f.userID).Scan(&name, &email, &suspended, &rssOn)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				s.fail(w, r, err)
				return
			}
			items = append(items, map[string]any{"feed": f, "userId": f.userID, "userName": name, "userEmail": email, "userRssEnabled": rssOn, "userSuspended": suspended})
		}
		respondNoStore(w, map[string]any{"items": items, "page": page, "limit": limit, "total": total})
	}))
	rt.Handle("PUT", "/admin/rss/feeds/{id}/enabled", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		enabled, ok := decodeEnabled(r)
		if !ok {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		feed, err := s.rssUpdate(r.Context(), nil, r.PathValue("id"), "enabled = $2, updated_at = $3", enabled, time.Now().UnixMilli())
		if err != nil {
			s.rssFail(w, r, err)
			return
		}
		respondNoStore(w, feed)
	}))
	rt.Handle("DELETE", "/admin/rss/feeds/{id}", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		if err := s.deleteRssFeed(r, r.PathValue("id"), nil); err != nil {
			s.rssFail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rt.Handle("PUT", "/admin/rss/users/{id}/enabled", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		enabled, ok := decodeEnabled(r)
		if !ok {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		user := r.PathValue("id")
		var exists bool
		if err := s.DB.QueryRow(r.Context(), "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", user).Scan(&exists); err != nil {
			s.fail(w, r, err)
			return
		}
		if !exists {
			s.rssFail(w, r, rssError{"User not found", "rss_user_not_found"})
			return
		}
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO rss_user_policies (user_id, enabled, updated_at) VALUES ($1, $2, $3)
ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = EXCLUDED.updated_at`, user, enabled, time.Now().UnixMilli()); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
