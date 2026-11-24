package api

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/portability"
	"typetype.dev/server/internal/store"
)

// portabilityPort is TypeTypePortabilityDataPort over this server's tables.
type portabilityPort struct{ s *Server }

// nameUUID is java.util.UUID.nameUUIDFromBytes: an MD5, version 3 UUID of the bytes.
func nameUUID(v string) string {
	b := md5.Sum([]byte(v))
	b[6] = b[6]&0x0f | 0x30
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// javaHash is String.hashCode over UTF-16 code units.
func javaHash(s string) int32 {
	var h int32
	for _, u := range utf16.Encode([]rune(s)) {
		h = 31*h + int32(u)
	}
	return h
}

// lockSubscriptions is SubscriptionMutationLock.acquire: one transaction-scoped advisory lock per user.
func lockSubscriptions(ctx context.Context, tx pgx.Tx, user string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(720815616, $1)", javaHash(user)&0x7fffffff)
	return err
}

func (p portabilityPort) Import(ctx context.Context, user string, src portability.Source, req portability.ImportRequest,
	progress func(portability.Category, int64) error, complete func(portability.Category, int64) error) (map[string]int64, error) {
	replace := req.DuplicatePolicy == "replace"
	out := map[string]int64{}
	for _, c := range req.Categories {
		one := func() error { return progress(c, 1) }
		var n int64
		var err error
		if c == portability.Settings {
			n, err = p.settings(ctx, user, src, one)
		} else {
			err = pgx.BeginFunc(ctx, p.s.DB, func(tx pgx.Tx) error {
				var e error
				n, e = p.write(ctx, tx, user, c, src, replace, one)
				return e
			})
		}
		if err != nil {
			return nil, err
		}
		out[string(c)] = n
		if err := complete(c, n); err != nil {
			return nil, err
		}
	}
	if has(req.Categories, portability.Subscriptions) {
		p.s.feeds.invalidate(user)
	}
	p.propagateAvatars(ctx, user)
	return out, nil
}

func has(cs []portability.Category, c portability.Category) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

func added(tag interface{ RowsAffected() int64 }, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (p portabilityPort) write(ctx context.Context, tx pgx.Tx, user string, c portability.Category, src portability.Source,
	replace bool, one func() error) (int64, error) {
	var count int64
	each := func(fn func(*portability.Record) (int64, error)) error {
		return src.ForEach(c, func(r *portability.Record) error {
			n, err := fn(r)
			if err != nil {
				return err
			}
			count += n
			return one()
		})
	}
	clear := func(tables ...string) error {
		if !replace {
			return nil
		}
		for _, t := range tables {
			if _, err := tx.Exec(ctx, "DELETE FROM "+t+" WHERE user_id = $1", user); err != nil {
				return err
			}
		}
		return nil
	}
	var err error
	switch c {
	case portability.Subscriptions:
		if err = lockSubscriptions(ctx, tx, user); err == nil {
			err = clear("subscriptions")
		}
		if err == nil {
			err = each(func(r *portability.Record) (int64, error) {
				if r.Type != portability.TypeSubscription {
					return 0, nil
				}
				return added(tx.Exec(ctx, `INSERT INTO subscriptions (user_id, channel_url, name, avatar_url, subscribed_at)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, user, db.CanonicalChannelURL(r.ChannelURL), r.Name, r.AvatarURL, r.SubscribedAt))
			})
		}
	case portability.SubscriptionGroups:
		err = p.groups(ctx, tx, user, src, replace, one, &count)
	case portability.Playlists:
		err = p.playlists(ctx, tx, user, src, replace, one, &count)
	case portability.History:
		err = p.history(ctx, tx, user, src, replace, one, &count)
	case portability.WatchLater:
		if err = clear("watch_later"); err == nil {
			err = each(func(r *portability.Record) (int64, error) {
				v := r.Video
				if r.Type != portability.TypeWatchLater || v == nil {
					return 0, nil
				}
				return added(tx.Exec(ctx, `INSERT INTO watch_later (user_id, url, title, thumbnail, duration, added_at, channel_name,
channel_url, channel_avatar, view_count, published_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`,
					user, v.URL, v.Title, v.ThumbnailURL, v.DurationSeconds, r.AddedAt, v.ChannelName, v.ChannelURL, v.ChannelAvatarURL, v.ViewCount, v.PublishedAt))
			})
		}
	case portability.Favorites:
		if err = clear("favorites"); err == nil {
			err = each(func(r *portability.Record) (int64, error) {
				v := r.Video
				if r.Type != portability.TypeFavorite || v == nil {
					return 0, nil
				}
				return added(tx.Exec(ctx, `INSERT INTO favorites (user_id, video_url, favorited_at, title, thumbnail, duration, channel_name,
channel_url, channel_avatar, view_count, published_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`,
					user, v.URL, r.FavoritedAt, v.Title, v.ThumbnailURL, v.DurationSeconds, v.ChannelName, v.ChannelURL, v.ChannelAvatarURL, v.ViewCount, v.PublishedAt))
			})
		}
	case portability.Progress:
		if err = clear("progress"); err == nil {
			err = each(func(r *portability.Record) (int64, error) {
				if r.Type != portability.TypeProgress {
					return 0, nil
				}
				return added(tx.Exec(ctx, "INSERT INTO progress (user_id, video_url, position, updated_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING",
					user, r.VideoURL, max(r.PositionSeconds, 0), r.UpdatedAt))
			})
		}
	case portability.SearchHistory:
		if err = clear("search_history"); err == nil {
			err = each(func(r *portability.Record) (int64, error) {
				if r.Type != portability.TypeSearchHistory {
					return 0, nil
				}
				var exists bool
				if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM search_history WHERE user_id = $1 AND term = $2 AND searched_at = $3)",
					user, r.Term, r.SearchedAt).Scan(&exists); err != nil || exists {
					return 0, err
				}
				return added(tx.Exec(ctx, "INSERT INTO search_history (id, user_id, term, searched_at) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING",
					auth.UUID(), user, r.Term, r.SearchedAt))
			})
		}
	case portability.SavedPlaylists:
		if err = clear("saved_playlists"); err == nil {
			err = each(func(r *portability.Record) (int64, error) {
				if r.Type != portability.TypeSavedPlaylist {
					return 0, nil
				}
				return added(tx.Exec(ctx, `INSERT INTO saved_playlists (id, user_id, public_playlist_id, url, title, thumbnail_url, uploader_name,
stream_count, playlist_type, saved_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING`,
					nameUUID(user+":saved:"+r.URL), user, r.SourceID, r.URL, r.Title, r.ThumbnailURL, r.UploaderName, r.StreamCount, r.PlaylistType, r.SavedAt))
			})
		}
	case portability.ContentFilters:
		err = p.filters(ctx, tx, user, src, replace, one, &count)
	}
	return count, err
}

func (p portabilityPort) groups(ctx context.Context, tx pgx.Tx, user string, src portability.Source, replace bool, one func() error, count *int64) error {
	if err := lockSubscriptions(ctx, tx, user); err != nil {
		return err
	}
	if replace {
		for _, t := range []string{"subscription_group_memberships", "subscription_groups"} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+t+" WHERE user_id = $1", user); err != nil {
				return err
			}
		}
	}
	err := src.ForEach(portability.SubscriptionGroups, func(r *portability.Record) error {
		if r.Type == portability.TypeGroup {
			norm := strings.ToLower(strings.TrimSpace(r.Name))
			if norm == "" || auth.JavaLength(norm) > 100 {
				return portabilityInvalid("Invalid subscription group name")
			}
			n, err := added(tx.Exec(ctx, `INSERT INTO subscription_groups (id, user_id, name, normalized_name, created_at, updated_at)
VALUES ($1, $2, $3, $4, 0, 0) ON CONFLICT DO NOTHING`, nameUUID(user+":group:"+norm), user, strings.TrimSpace(r.Name), norm))
			if err != nil {
				return err
			}
			*count += n
		}
		return one()
	})
	if err != nil {
		return err
	}
	return src.ForEach(portability.SubscriptionGroups, func(r *portability.Record) error {
		if r.Type == portability.TypeGroupMembership {
			var group string
			err := tx.QueryRow(ctx, "SELECT id FROM subscription_groups WHERE user_id = $1 AND normalized_name = $2",
				user, strings.ToLower(strings.TrimSpace(r.GroupName))).Scan(&group)
			if err == nil {
				channel := db.CanonicalChannelURL(r.ChannelURL)
				n, err := added(tx.Exec(ctx, `INSERT INTO subscription_group_memberships (group_id, user_id, channel_url, added_at)
SELECT $1, $2, $3, 0 WHERE EXISTS (SELECT 1 FROM subscriptions WHERE user_id = $2 AND channel_url = $3) ON CONFLICT DO NOTHING`, group, user, channel))
				if err != nil {
					return err
				}
				*count += n
			} else if err != pgx.ErrNoRows {
				return err
			}
		}
		return one()
	})
}

func (p portabilityPort) playlists(ctx context.Context, tx pgx.Tx, user string, src portability.Source, replace bool, one func() error, count *int64) error {
	if replace {
		for _, t := range []string{"playlist_videos", "playlists"} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+t+" WHERE user_id = $1", user); err != nil {
				return err
			}
		}
	}
	err := src.ForEach(portability.Playlists, func(r *portability.Record) error {
		if r.Type != portability.TypePlaylist {
			return nil
		}
		n, err := added(tx.Exec(ctx, "INSERT INTO playlists (id, user_id, name, description, created_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING",
			nameUUID(user+":playlist:"+r.SourceID), user, r.Name, r.Description, r.CreatedAt))
		*count += n
		return err
	})
	if err != nil {
		return err
	}
	return src.ForEach(portability.Playlists, func(r *portability.Record) error {
		if v := r.Video; r.Type == portability.TypePlaylistVideo && v != nil {
			n, err := added(tx.Exec(ctx, `INSERT INTO playlist_videos (id, playlist_id, user_id, url, title, thumbnail, duration, position,
channel_name, channel_url, channel_avatar, view_count, added_at, published_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) ON CONFLICT DO NOTHING`,
				nameUUID(fmt.Sprintf("%s:playlist:%s:%d:%s", user, r.PlaylistSourceID, r.Position, v.URL)), nameUUID(user+":playlist:"+r.PlaylistSourceID),
				user, v.URL, v.Title, v.ThumbnailURL, v.DurationSeconds, r.Position, v.ChannelName, v.ChannelURL, v.ChannelAvatarURL, v.ViewCount, r.AddedAt, v.PublishedAt))
			if err != nil {
				return err
			}
			*count += n
		}
		return one()
	})
}

type historyKey struct {
	url string
	at  int64
}

func (p portabilityPort) history(ctx context.Context, tx pgx.Tx, user string, src portability.Source, replace bool, one func() error, count *int64) error {
	known := map[historyKey]bool{}
	if replace {
		if _, err := tx.Exec(ctx, "DELETE FROM history WHERE user_id = $1", user); err != nil {
			return err
		}
	} else {
		rows, err := tx.Query(ctx, "SELECT url, watched_at FROM history WHERE user_id = $1", user)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k historyKey
			if rows.Scan(&k.url, &k.at) == nil {
				known[k] = true
			}
		}
		rows.Close()
	}
	var batch pgx.Batch
	flush := func() error {
		if batch.Len() == 0 {
			return nil
		}
		n := batch.Len()
		err := tx.SendBatch(ctx, &batch).Close()
		batch = pgx.Batch{}
		*count += int64(n)
		return err
	}
	err := src.ForEach(portability.History, func(r *portability.Record) error {
		if v := r.Video; r.Type == portability.TypeHistory && v != nil && !known[historyKey{v.URL, r.WatchedAt}] {
			known[historyKey{v.URL, r.WatchedAt}] = true
			batch.Queue(`INSERT INTO history (id, user_id, url, title, thumbnail, channel_name, channel_url, channel_avatar, duration, progress, watched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, auth.UUID(), user, v.URL, v.Title, v.ThumbnailURL, v.ChannelName, v.ChannelURL,
				v.ChannelAvatarURL, v.DurationSeconds, r.PositionSeconds, r.WatchedAt)
		}
		if err := one(); err != nil {
			return err
		}
		if batch.Len() >= 500 {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}

func (p portabilityPort) filters(ctx context.Context, tx pgx.Tx, user string, src portability.Source, replace bool, one func() error, count *int64) error {
	if replace {
		for _, t := range []string{"blocked_channels", "blocked_videos", "blocked_keywords", "allowed_channels", "allowed_playlists"} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+t+" WHERE user_id = $1 AND scope = 'user'", user); err != nil {
				return err
			}
		}
	}
	return src.ForEach(portability.ContentFilters, func(r *portability.Record) error {
		if r.Type != portability.TypeContentFilter {
			return one()
		}
		var n int64
		var err error
		switch r.Kind {
		case "blockedChannel":
			n, err = added(tx.Exec(ctx, `INSERT INTO blocked_channels (user_id, scope, channel_url, name, thumbnail_url, blocked_at)
VALUES ($1, 'user', $2, $3, $4, $5) ON CONFLICT DO NOTHING`, user, r.Value, r.Label, r.ImageURL, r.CreatedAt))
		case "blockedVideo":
			n, err = added(tx.Exec(ctx, "INSERT INTO blocked_videos (user_id, scope, video_url, blocked_at) VALUES ($1, 'user', $2, $3) ON CONFLICT DO NOTHING",
				user, r.Value, r.CreatedAt))
		case "blockedKeyword":
			n, err = added(tx.Exec(ctx, "INSERT INTO blocked_keywords (user_id, scope, keyword, blocked_at) VALUES ($1, 'user', $2, $3) ON CONFLICT DO NOTHING",
				user, normalizeKeyword(r.Value), r.CreatedAt))
		case "allowedChannel":
			n, err = added(tx.Exec(ctx, `INSERT INTO allowed_channels (user_id, scope, channel_url, name, thumbnail_url, allowed_at)
VALUES ($1, 'user', $2, $3, $4, $5) ON CONFLICT DO NOTHING`, user, channelKey(r.Value), r.Label, r.ImageURL, r.CreatedAt))
		case "allowedPlaylist":
			var meta struct {
				UploaderName string `json:"uploaderName"`
			}
			json.Unmarshal(r.Metadata, &meta)
			n, err = added(tx.Exec(ctx, `INSERT INTO allowed_playlists (user_id, scope, playlist_url, title, thumbnail_url, uploader_name, allowed_at)
SELECT $1, 'user', $2, $3, $4, $5, $6 WHERE NOT EXISTS (SELECT 1 FROM allowed_playlists WHERE user_id = $1 AND scope = 'user' AND playlist_url = $2)`,
				user, playlistKey(r.Value), r.Label, r.ImageURL, meta.UploaderName, r.CreatedAt))
		default:
			return portabilityInvalid("Unsupported content filter kind")
		}
		if err != nil {
			return err
		}
		*count += n
		return one()
	})
}

func (p portabilityPort) settings(ctx context.Context, user string, src portability.Source, one func() error) (int64, error) {
	var count int64
	err := src.ForEach(portability.Settings, func(r *portability.Record) error {
		switch r.Type {
		case portability.TypeSettings:
			st := store.DefaultSettings()
			if err := json.Unmarshal(r.Values, &st); err != nil {
				return portabilityInvalid("Invalid settings")
			}
			st.Normalize()
			if _, err := store.UpsertSettings(ctx, p.s.DB, user, st); err != nil {
				return err
			}
			count++
		case portability.TypeRecommender:
			n, err := p.restoreRecommender(ctx, user, r.Values)
			if err != nil {
				return err
			}
			count += n
		}
		return one()
	})
	return count, err
}

func portabilityInvalid(msg string) error {
	return &portability.Error{Status: 400, Code: "portability_invalid_input", Message: msg}
}
