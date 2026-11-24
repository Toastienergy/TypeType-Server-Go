package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/portability"
	"typetype.dev/server/internal/store"
)

// propagateAvatars is SubscriptionAvatarRepairer.repairImported then PortabilityMediaAvatarPropagation: a
// subscription without avatar takes the newest one its channel has in the library, then every library row of a
// subscribed channel without avatar takes the subscription's.
func (p portabilityPort) propagateAvatars(ctx context.Context, user string) {
	p.s.DB.Exec(ctx, `UPDATE subscriptions s SET avatar_url = src.avatar FROM (
  SELECT DISTINCT ON (channel_url) channel_url, avatar FROM (
    SELECT channel_url, channel_avatar AS avatar, 1 AS k, watched_at AS at FROM history WHERE user_id = $1 AND channel_avatar <> ''
    UNION ALL SELECT channel_url, channel_avatar, 2, added_at FROM playlist_videos WHERE user_id = $1 AND channel_avatar <> ''
    UNION ALL SELECT channel_url, channel_avatar, 3, added_at FROM watch_later WHERE user_id = $1 AND channel_avatar <> ''
    UNION ALL SELECT channel_url, channel_avatar, 4, favorited_at FROM favorites WHERE user_id = $1 AND channel_avatar <> ''
  ) a ORDER BY channel_url, k, at DESC
) src WHERE s.user_id = $1 AND s.avatar_url = '' AND s.channel_url = src.channel_url`, user)
	for _, t := range []string{"history", "playlist_videos", "watch_later", "favorites"} {
		p.s.DB.Exec(ctx, "UPDATE "+t+` t SET channel_avatar = s.avatar_url FROM subscriptions s
WHERE t.user_id = $1 AND s.user_id = $1 AND s.channel_url = t.channel_url AND t.channel_avatar = '' AND s.avatar_url <> ''`, user)
	}
}

type recommenderFeedback struct {
	ID           string                     `json:"id"`
	Kind         string                     `json:"kind"`
	Verdict      string                     `json:"verdict"`
	Comment      *string                    `json:"comment"`
	VideoURL     *string                    `json:"videoUrl"`
	Reason       *string                    `json:"reason"`
	Title        *string                    `json:"title"`
	ChannelName  *string                    `json:"channelName"`
	ThumbnailURL *string                    `json:"thumbnailUrl"`
	AlgorithmID  string                     `json:"algorithmId"`
	Parameters   map[string]json.RawMessage `json:"parameters"`
	CreatedAt    int64                      `json:"createdAt"`
}

type recommenderBackup struct {
	Settings json.RawMessage       `json:"settings"`
	Feedback []recommenderFeedback `json:"feedback"`
}

func (p portabilityPort) exportRecommender(ctx context.Context, user string) (json.RawMessage, error) {
	var b recommenderBackup
	var settings string
	err := p.s.DB.QueryRow(ctx, "SELECT settings_json FROM recommender_user_settings WHERE user_id = $1", user).Scan(&settings)
	if err == nil && json.Valid([]byte(settings)) {
		b.Settings = json.RawMessage(settings)
	} else if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	rows, err := p.s.DB.Query(ctx, `SELECT id, kind, verdict, comment, video_url, reason, title, channel_name, thumbnail_url, algorithm_id,
parameters_json, created_at FROM recommender_feedback WHERE user_id = $1 ORDER BY created_at DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	b.Feedback = []recommenderFeedback{}
	for rows.Next() {
		var f recommenderFeedback
		var params string
		if err := rows.Scan(&f.ID, &f.Kind, &f.Verdict, &f.Comment, &f.VideoURL, &f.Reason, &f.Title, &f.ChannelName, &f.ThumbnailURL,
			&f.AlgorithmID, &params, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.Parameters = map[string]json.RawMessage{}
		json.Unmarshal([]byte(params), &f.Parameters)
		b.Feedback = append(b.Feedback, f)
	}
	if b.Settings == nil && len(b.Feedback) == 0 {
		return nil, rows.Err()
	}
	if b.Settings == nil {
		b.Settings = json.RawMessage("null")
	}
	out, _ := json.Marshal(b)
	return out, rows.Err()
}

// restoreRecommender is RecommenderUserDataRepository.restore: settings replace, and each opinion not already
// held (ignoring its id) comes in under a fresh id.
func (p portabilityPort) restoreRecommender(ctx context.Context, user string, raw json.RawMessage) (int64, error) {
	var b recommenderBackup
	if err := json.Unmarshal(raw, &b); err != nil {
		return 0, portabilityInvalid("Invalid recommender data")
	}
	var n int64
	if len(b.Settings) > 0 && string(b.Settings) != "null" {
		var s struct {
			Enabled     bool    `json:"enabled"`
			AlgorithmID *string `json:"algorithmId"`
		}
		json.Unmarshal(b.Settings, &s)
		if _, err := p.s.DB.Exec(ctx, `INSERT INTO recommender_user_settings (user_id, enabled, algorithm_id, settings_json, updated_at)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled, algorithm_id = EXCLUDED.algorithm_id,
settings_json = EXCLUDED.settings_json, updated_at = EXCLUDED.updated_at`, user, s.Enabled, s.AlgorithmID, string(b.Settings), time.Now().UnixMilli()); err != nil {
			return 0, err
		}
		n++
	}
	existing, err := p.exportRecommender(ctx, user)
	if err != nil {
		return 0, err
	}
	var have recommenderBackup
	json.Unmarshal(existing, &have)
	key := func(f recommenderFeedback) string {
		f.ID = ""
		b, _ := json.Marshal(f)
		return string(b)
	}
	known := map[string]bool{}
	for _, f := range have.Feedback {
		known[key(f)] = true
	}
	for _, f := range b.Feedback {
		if f.Parameters == nil {
			f.Parameters = map[string]json.RawMessage{}
		}
		k := key(f)
		if known[k] {
			continue
		}
		known[k] = true
		params, _ := json.Marshal(f.Parameters)
		if _, err := p.s.DB.Exec(ctx, `INSERT INTO recommender_feedback (id, user_id, kind, verdict, comment, video_url, reason, title,
channel_name, thumbnail_url, algorithm_id, parameters_json, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT DO NOTHING`, auth.UUID(), user, f.Kind, f.Verdict, f.Comment, f.VideoURL, f.Reason, f.Title, f.ChannelName, f.ThumbnailURL,
			f.AlgorithmID, string(params), f.CreatedAt); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

func video(url, title, thumb string, duration int64, channel, channelURL, avatar string, views, published int64) *portability.Video {
	return &portability.Video{URL: url, Title: title, ThumbnailURL: thumb, DurationSeconds: duration, ChannelName: channel,
		ChannelURL: channelURL, ChannelAvatarURL: avatar, ViewCount: views, PublishedAt: published}
}

// Export is TypeTypePortabilityExport: every category in the Kotlin order, oldest first.
func (p portabilityPort) Export(ctx context.Context, user string, categories []portability.Category, sink portability.Sink) error {
	put := func(r *portability.Record) error {
		_, err := sink.Write(r)
		return err
	}
	scan := func(q string, fn func(pgx.Rows) (*portability.Record, error), args ...any) error {
		rows, err := p.s.DB.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		var recs []*portability.Record
		for rows.Next() {
			r, err := fn(rows)
			if err != nil {
				rows.Close()
				return err
			}
			recs = append(recs, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range recs {
			if err := put(r); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range categories {
		if err := sink.MarkCategory(c); err != nil {
			return err
		}
		var err error
		switch c {
		case portability.Subscriptions:
			err = scan("SELECT channel_url, name, avatar_url, subscribed_at FROM subscriptions WHERE user_id = $1 ORDER BY subscribed_at",
				func(r pgx.Rows) (*portability.Record, error) {
					x := &portability.Record{Type: portability.TypeSubscription}
					return x, r.Scan(&x.ChannelURL, &x.Name, &x.AvatarURL, &x.SubscribedAt)
				}, user)
		case portability.SubscriptionGroups:
			err = p.exportGroups(ctx, user, put)
		case portability.History:
			err = scan(`SELECT url, title, thumbnail, duration, channel_name, channel_url, channel_avatar, watched_at, progress FROM history
WHERE user_id = $1 ORDER BY watched_at`, func(r pgx.Rows) (*portability.Record, error) {
				var u, t, th, cn, cu, ca string
				var d int64
				x := &portability.Record{Type: portability.TypeHistory}
				err := r.Scan(&u, &t, &th, &d, &cn, &cu, &ca, &x.WatchedAt, &x.PositionSeconds)
				x.Video = video(u, t, th, d, cn, cu, ca, 0, -1)
				return x, err
			}, user)
		case portability.Playlists:
			err = p.exportPlaylists(ctx, user, put)
		case portability.WatchLater:
			err = scan(`SELECT url, title, thumbnail, duration, channel_name, channel_url, channel_avatar, view_count, published_at, added_at
FROM watch_later WHERE user_id = $1 ORDER BY added_at`, func(r pgx.Rows) (*portability.Record, error) {
				x := &portability.Record{Type: portability.TypeWatchLater, Video: &portability.Video{}}
				v := x.Video
				return x, r.Scan(&v.URL, &v.Title, &v.ThumbnailURL, &v.DurationSeconds, &v.ChannelName, &v.ChannelURL, &v.ChannelAvatarURL, &v.ViewCount, &v.PublishedAt, &x.AddedAt)
			}, user)
		case portability.Favorites:
			err = scan(`SELECT video_url, title, thumbnail, duration, channel_name, channel_url, channel_avatar, view_count, published_at, favorited_at
FROM favorites WHERE user_id = $1 ORDER BY favorited_at`, func(r pgx.Rows) (*portability.Record, error) {
				x := &portability.Record{Type: portability.TypeFavorite, Video: &portability.Video{}}
				v := x.Video
				return x, r.Scan(&v.URL, &v.Title, &v.ThumbnailURL, &v.DurationSeconds, &v.ChannelName, &v.ChannelURL, &v.ChannelAvatarURL, &v.ViewCount, &v.PublishedAt, &x.FavoritedAt)
			}, user)
		case portability.Progress:
			err = scan("SELECT video_url, position, updated_at FROM progress WHERE user_id = $1 ORDER BY updated_at",
				func(r pgx.Rows) (*portability.Record, error) {
					x := &portability.Record{Type: portability.TypeProgress}
					return x, r.Scan(&x.VideoURL, &x.PositionSeconds, &x.UpdatedAt)
				}, user)
		case portability.SearchHistory:
			err = scan("SELECT term, searched_at FROM search_history WHERE user_id = $1 ORDER BY searched_at",
				func(r pgx.Rows) (*portability.Record, error) {
					x := &portability.Record{Type: portability.TypeSearchHistory}
					return x, r.Scan(&x.Term, &x.SearchedAt)
				}, user)
		case portability.SavedPlaylists:
			err = scan(`SELECT public_playlist_id, url, title, thumbnail_url, uploader_name, stream_count, playlist_type, saved_at FROM saved_playlists
WHERE user_id = $1 ORDER BY saved_at`, func(r pgx.Rows) (*portability.Record, error) {
				x := &portability.Record{Type: portability.TypeSavedPlaylist}
				return x, r.Scan(&x.SourceID, &x.URL, &x.Title, &x.ThumbnailURL, &x.UploaderName, &x.StreamCount, &x.PlaylistType, &x.SavedAt)
			}, user)
		case portability.Settings:
			err = p.exportSettings(ctx, user, put)
		case portability.ContentFilters:
			err = p.exportFilters(ctx, user, scan)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// exportGroups writes each group, oldest first, followed by its members in the order they were added.
func (p portabilityPort) exportGroups(ctx context.Context, user string, put func(*portability.Record) error) error {
	rows, err := p.s.DB.Query(ctx, "SELECT id, name FROM subscription_groups WHERE user_id = $1 ORDER BY created_at", user)
	if err != nil {
		return err
	}
	type group struct{ id, name string }
	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.id, &g.name); err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, g)
	}
	rows.Close()
	for _, g := range groups {
		if err := put(&portability.Record{Type: portability.TypeGroup, Name: g.name}); err != nil {
			return err
		}
		mrows, err := p.s.DB.Query(ctx, "SELECT channel_url FROM subscription_group_memberships WHERE group_id = $1 ORDER BY added_at", g.id)
		if err != nil {
			return err
		}
		var urls []string
		for mrows.Next() {
			var u string
			if mrows.Scan(&u) == nil {
				urls = append(urls, u)
			}
		}
		mrows.Close()
		for _, u := range urls {
			if err := put(&portability.Record{Type: portability.TypeGroupMembership, GroupName: g.name, ChannelURL: u}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p portabilityPort) exportPlaylists(ctx context.Context, user string, put func(*portability.Record) error) error {
	rows, err := p.s.DB.Query(ctx, "SELECT id, name, description, created_at FROM playlists WHERE user_id = $1 ORDER BY created_at", user)
	if err != nil {
		return err
	}
	var lists []*portability.Record
	for rows.Next() {
		x := &portability.Record{Type: portability.TypePlaylist}
		if err := rows.Scan(&x.SourceID, &x.Name, &x.Description, &x.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		lists = append(lists, x)
	}
	rows.Close()
	for _, l := range lists {
		if err := put(l); err != nil {
			return err
		}
		vrows, err := p.s.DB.Query(ctx, `SELECT position, url, title, thumbnail, duration, channel_name, channel_url, channel_avatar, view_count,
published_at, added_at FROM playlist_videos WHERE user_id = $1 AND playlist_id = $2 ORDER BY position`, user, l.SourceID)
		if err != nil {
			return err
		}
		var vids []*portability.Record
		for vrows.Next() {
			x := &portability.Record{Type: portability.TypePlaylistVideo, PlaylistSourceID: l.SourceID, Video: &portability.Video{}}
			v := x.Video
			if err := vrows.Scan(&x.Position, &v.URL, &v.Title, &v.ThumbnailURL, &v.DurationSeconds, &v.ChannelName, &v.ChannelURL,
				&v.ChannelAvatarURL, &v.ViewCount, &v.PublishedAt, &x.AddedAt); err != nil {
				vrows.Close()
				return err
			}
			vids = append(vids, x)
		}
		vrows.Close()
		for _, v := range vids {
			if err := put(v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p portabilityPort) exportSettings(ctx context.Context, user string, put func(*portability.Record) error) error {
	st, err := store.GetSettings(ctx, p.s.DB, user)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(st)
	if err := put(&portability.Record{Type: portability.TypeSettings, Values: b}); err != nil {
		return err
	}
	rec, err := p.exportRecommender(ctx, user)
	if err != nil || rec == nil {
		return err
	}
	return put(&portability.Record{Type: portability.TypeRecommender, Values: rec})
}

func (p portabilityPort) exportFilters(ctx context.Context, user string, scan func(string, func(pgx.Rows) (*portability.Record, error), ...any) error) error {
	filter := func(kind string) *portability.Record {
		return &portability.Record{Type: portability.TypeContentFilter, Kind: kind}
	}
	opt := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	steps := []func() error{
		func() error {
			return scan("SELECT channel_url, name, thumbnail_url, blocked_at FROM blocked_channels WHERE user_id = $1 AND scope = 'user'",
				func(r pgx.Rows) (*portability.Record, error) {
					x := filter("blockedChannel")
					var n, t *string
					err := r.Scan(&x.Value, &n, &t, &x.CreatedAt)
					x.Label, x.ImageURL = opt(n), opt(t)
					return x, err
				}, user)
		},
		func() error {
			return scan("SELECT video_url, blocked_at FROM blocked_videos WHERE user_id = $1 AND scope = 'user'",
				func(r pgx.Rows) (*portability.Record, error) {
					x := filter("blockedVideo")
					return x, r.Scan(&x.Value, &x.CreatedAt)
				}, user)
		},
		func() error {
			return scan("SELECT keyword, blocked_at FROM blocked_keywords WHERE user_id = $1 AND scope = 'user'",
				func(r pgx.Rows) (*portability.Record, error) {
					x := filter("blockedKeyword")
					return x, r.Scan(&x.Value, &x.CreatedAt)
				}, user)
		},
		func() error {
			return scan("SELECT channel_url, name, thumbnail_url, allowed_at FROM allowed_channels WHERE user_id = $1 AND scope = 'user'",
				func(r pgx.Rows) (*portability.Record, error) {
					x := filter("allowedChannel")
					var n, t *string
					err := r.Scan(&x.Value, &n, &t, &x.CreatedAt)
					x.Label, x.ImageURL = opt(n), opt(t)
					return x, err
				}, user)
		},
		func() error {
			return scan("SELECT playlist_url, title, thumbnail_url, uploader_name, allowed_at FROM allowed_playlists WHERE user_id = $1 AND scope = 'user'",
				func(r pgx.Rows) (*portability.Record, error) {
					x := filter("allowedPlaylist")
					var t, th, u *string
					err := r.Scan(&x.Value, &t, &th, &u, &x.CreatedAt)
					x.Label, x.ImageURL = opt(t), opt(th)
					x.Metadata, _ = json.Marshal(map[string]string{"uploaderName": opt(u)})
					return x, err
				}, user)
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}
