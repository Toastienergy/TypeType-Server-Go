package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
)

type rssFeed struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Scope           string   `json:"scope"`
	ChannelURLs     []string `json:"channelUrls"`
	ServiceIDs      []int    `json:"serviceIds"`
	IncludeVideos   bool     `json:"includeVideos"`
	IncludeShorts   bool     `json:"includeShorts"`
	IncludeLive     bool     `json:"includeLive"`
	IncludeUpcoming bool     `json:"includeUpcoming"`
	Enabled         bool     `json:"enabled"`
	CreatedAt       int64    `json:"createdAt"`
	UpdatedAt       int64    `json:"updatedAt"`
	LastUsedAt      *int64   `json:"lastUsedAt"`
	userID          string
	tokenHash       string
}

// rssRequest is RssFeedRequest with its defaults; a field sent as null is refused, as kotlinx does.
type rssRequest struct {
	Name            string   `json:"name"`
	Scope           string   `json:"scope"`
	ChannelURLs     []string `json:"channelUrls"`
	ServiceIDs      []int    `json:"serviceIds"`
	IncludeVideos   bool     `json:"includeVideos"`
	IncludeShorts   bool     `json:"includeShorts"`
	IncludeLive     bool     `json:"includeLive"`
	IncludeUpcoming bool     `json:"includeUpcoming"`
}

type rssError struct{ msg, code string }

func (e rssError) Error() string { return e.msg }

var errRssNotFound = rssError{"RSS feed not found", "rss_feed_not_found"}

func (s *Server) rssFail(w http.ResponseWriter, r *http.Request, err error) {
	var re rssError
	if !errors.As(err, &re) {
		s.fail(w, r, err)
		return
	}
	status := http.StatusBadRequest
	switch re.code {
	case "rss_feed_not_found", "rss_user_not_found":
		status = http.StatusNotFound
	case "rss_disabled", "rss_user_disabled":
		status = http.StatusForbidden
	case "rss_feed_limit_reached":
		status = http.StatusConflict
	}
	httpx.Error(w, r, status, re.msg, re.code)
}

func decodeRssRequest(r *http.Request) (rssRequest, bool) {
	var raw map[string]json.RawMessage
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&raw) != nil || raw == nil {
		return rssRequest{}, false
	}
	for _, k := range []string{"name", "scope", "channelUrls", "serviceIds", "includeVideos", "includeShorts", "includeLive", "includeUpcoming"} {
		if v, ok := raw[k]; ok && string(v) == "null" {
			return rssRequest{}, false
		}
	}
	if _, ok := raw["name"]; !ok {
		return rssRequest{}, false
	}
	req := rssRequest{Scope: "all", ChannelURLs: []string{}, ServiceIDs: []int{0, 5, 6}, IncludeVideos: true, IncludeShorts: true, IncludeLive: true, IncludeUpcoming: true}
	b, _ := json.Marshal(raw)
	return req, json.Unmarshal(b, &req) == nil
}

func (s *Server) rssAvailable(ctx context.Context, user string) (string, int, error) {
	st, err := s.Admin.Get(ctx)
	if err != nil {
		return "", 0, err
	}
	if !st.RssEnabled || st.RssPublicBaseURL == nil {
		return "", 0, rssError{"RSS feeds are disabled", "rss_disabled"}
	}
	ok, err := s.rssUserEnabled(ctx, user)
	if err != nil {
		return "", 0, err
	}
	if !ok {
		return "", 0, rssError{"RSS feeds are disabled for this account", "rss_user_disabled"}
	}
	return *st.RssPublicBaseURL, st.RssMaxFeedsPerUser, nil
}

func (s *Server) rssUserEnabled(ctx context.Context, user string) (bool, error) {
	var suspended bool
	if err := s.DB.QueryRow(ctx, "SELECT suspended FROM users WHERE id = $1", user).Scan(&suspended); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil || suspended {
		return false, err
	}
	enabled := true
	if err := s.DB.QueryRow(ctx, "SELECT enabled FROM rss_user_policies WHERE user_id = $1", user).Scan(&enabled); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	return enabled, nil
}

// normalizeRss is RssFeedManagementService.normalize, checks in the same order.
func (s *Server) normalizeRss(ctx context.Context, user string, req rssRequest) (rssRequest, error) {
	req.Name = strings.TrimSpace(req.Name)
	if n := len(utf16.Encode([]rune(req.Name))); n < 1 || n > 100 {
		return req, rssError{"Name must contain 1 to 100 characters", "rss_invalid_name"}
	}
	if req.Scope != "all" && req.Scope != "channels" {
		return req, rssError{"Invalid RSS scope", "rss_invalid_scope"}
	}
	services := slices.Compact(slices.Sorted(slices.Values(req.ServiceIDs)))
	if len(services) == 0 || slices.ContainsFunc(services, func(id int) bool { return id != 0 && id != 5 && id != 6 }) {
		return req, rssError{"Select at least one supported service", "rss_invalid_services"}
	}
	req.ServiceIDs = services
	if !req.IncludeVideos && !req.IncludeShorts && !req.IncludeLive && !req.IncludeUpcoming {
		return req, rssError{"Select at least one content type", "rss_invalid_types"}
	}
	if req.Scope == "all" {
		req.ChannelURLs = []string{}
		return req, nil
	}
	var channels []string
	for _, c := range req.ChannelURLs {
		if c = db.CanonicalChannelURL(c); strings.TrimSpace(c) != "" && !slices.Contains(channels, c) {
			channels = append(channels, c)
		}
	}
	if len(channels) == 0 || len(channels) > 100 {
		return req, rssError{"Select between 1 and 100 subscribed channels", "rss_invalid_channels"}
	}
	rows, err := s.DB.Query(ctx, "SELECT channel_url FROM subscriptions WHERE user_id = $1", user)
	if err != nil {
		return req, err
	}
	subscribed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return req, err
	}
	for _, c := range channels {
		if !slices.Contains(subscribed, c) {
			return req, rssError{"RSS channels must belong to your subscriptions", "rss_channel_not_subscribed"}
		}
	}
	slices.Sort(channels)
	req.ChannelURLs = channels
	return req, nil
}

const rssFeedColumns = "id, user_id, name, token_hash, scope, include_videos, include_shorts, include_live, include_upcoming, enabled, created_at, updated_at, last_used_at"

// rssFeeds loads feeds with their channel and service selections.
func rssFeeds(ctx context.Context, q pgxQuerier, where string, args ...any) ([]*rssFeed, error) {
	rows, err := q.Query(ctx, "SELECT "+rssFeedColumns+" FROM rss_feeds "+where, args...)
	if err != nil {
		return nil, err
	}
	var feeds []*rssFeed
	byID := map[string]*rssFeed{}
	var ids []string
	for rows.Next() {
		f := &rssFeed{ChannelURLs: []string{}, ServiceIDs: []int{}}
		if err := rows.Scan(&f.ID, &f.userID, &f.Name, &f.tokenHash, &f.Scope, &f.IncludeVideos, &f.IncludeShorts, &f.IncludeLive, &f.IncludeUpcoming,
			&f.Enabled, &f.CreatedAt, &f.UpdatedAt, &f.LastUsedAt); err != nil {
			rows.Close()
			return nil, err
		}
		feeds = append(feeds, f)
		byID[f.ID] = f
		ids = append(ids, f.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return feeds, err
	}
	rows, err = q.Query(ctx, `SELECT feed_id, channel_url, NULL::int FROM rss_feed_channels WHERE feed_id = ANY($1)
UNION ALL SELECT feed_id, NULL, service_id FROM rss_feed_services WHERE feed_id = ANY($1) ORDER BY 1, 2, 3`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var channel *string
		var service *int
		if err := rows.Scan(&id, &channel, &service); err != nil {
			return nil, err
		}
		if f := byID[id]; f != nil && channel != nil {
			f.ChannelURLs = append(f.ChannelURLs, *channel)
		} else if f != nil && service != nil {
			f.ServiceIDs = append(f.ServiceIDs, *service)
		}
	}
	return feeds, rows.Err()
}

func rssFeedByID(ctx context.Context, q pgxQuerier, id string) (*rssFeed, error) {
	feeds, err := rssFeeds(ctx, q, "WHERE id = $1", id)
	if err != nil || len(feeds) == 0 {
		return nil, err
	}
	return feeds[0], nil
}

func replaceRssSelections(ctx context.Context, tx pgx.Tx, id string, req rssRequest) error {
	batch := &pgx.Batch{}
	batch.Queue("DELETE FROM rss_feed_channels WHERE feed_id = $1", id)
	for _, c := range req.ChannelURLs {
		batch.Queue("INSERT INTO rss_feed_channels (feed_id, channel_url) VALUES ($1, $2)", id, c)
	}
	batch.Queue("DELETE FROM rss_feed_services WHERE feed_id = $1", id)
	for _, sid := range req.ServiceIDs {
		batch.Queue("INSERT INTO rss_feed_services (feed_id, service_id) VALUES ($1, $2)", id, sid)
	}
	return tx.SendBatch(ctx, batch).Close()
}

func rssSecret() (string, string) {
	b := make([]byte, 32)
	rand.Read(b)
	secret := base64.RawURLEncoding.EncodeToString(b)
	return secret, rssHash(secret)
}

func rssHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func rssFeedURL(base, id, secret string) string {
	return base + "/api/rss/feeds/" + id + ".xml?token=" + url.QueryEscape(secret)
}

// rssUpdate runs one change on a feed the user owns and answers the feed as stored after it.
func (s *Server) rssUpdate(ctx context.Context, owner *string, id, set string, args ...any) (*rssFeed, error) {
	var out *rssFeed
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		where, params := "WHERE id = $1", append([]any{id}, args...)
		if owner != nil {
			where += " AND user_id = $" + strconv.Itoa(len(params)+1)
			params = append(params, *owner)
		}
		tag, err := tx.Exec(ctx, "UPDATE rss_feeds SET "+set+" "+where, params...)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		out, err = rssFeedByID(ctx, tx, id)
		return err
	})
	if err == nil && out == nil {
		err = errRssNotFound
	}
	return out, err
}

func respondNoStore(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", instanceCacheControl)
	httpx.JSON(w, http.StatusOK, v)
}

func (s *Server) rssUser(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if strings.HasPrefix(user, "guest:") {
			plain(w, r, http.StatusForbidden, "Guest users cannot manage RSS feeds")
			return
		}
		next(w, r, user)
	})
}

func rssBodyError(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusBadRequest, "Invalid request body", "rss_invalid_body")
}

type pgxQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}
