package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/portability"
)

type takeoutPlan struct {
	ImportSubscriptions bool `json:"importSubscriptions"`
	ImportPlaylists     bool `json:"importPlaylists"`
	ImportPlaylistItems bool `json:"importPlaylistItems"`
	ImportFavorites     bool `json:"importFavorites"`
	ImportWatchLater    bool `json:"importWatchLater"`
	ImportHistory       bool `json:"importHistory"`
}

// takeoutCommit is YoutubeTakeoutImportJobService.commit: once per job, the import running in the background.
func (s *Server) takeoutCommit(ctx context.Context, user, id string, plan takeoutPlan) (*takeoutStatus, error) {
	var started, completed bool
	err := s.DB.QueryRow(ctx, "SELECT import_started, import_completed FROM youtube_takeout_import_jobs WHERE id = $1 AND user_id = $2", id, user).Scan(&started, &completed)
	if err != nil {
		return nil, errors.New("Import job not found")
	}
	if !started && !completed {
		s.DB.Exec(ctx, "UPDATE youtube_takeout_import_jobs SET import_started = TRUE, updated_at = $2 WHERE id = $1", id, time.Now().UnixMilli())
		s.setTakeoutStatus(ctx, id, "running", "importing", 0)
		go s.runTakeoutCommit(user, id, plan)
	}
	st, err := s.takeoutStatus(ctx, user, id)
	if err != nil || st == nil {
		return nil, errors.New("Missing job")
	}
	return st, nil
}

func (s *Server) runTakeoutCommit(user, id string, plan takeoutPlan) {
	ctx := context.Background()
	defer takeoutParsed.Delete(id)
	if _, err := s.takeoutPreview(ctx, user, id); err != nil {
		s.failTakeout(ctx, id, "import_failed", "IllegalArgumentException: "+err.Error())
		return
	}
	s.setTakeoutStatus(ctx, id, "running", "importing", 0)
	parsed, err := s.takeoutParse(ctx, user, id)
	if err != nil {
		s.failTakeout(ctx, id, "import_failed", "IllegalArgumentException: "+err.Error())
		return
	}
	report, err := s.importTakeout(ctx, user, id, parsed, plan)
	if err != nil {
		s.failTakeout(ctx, id, "import_failed", "Exception: "+err.Error())
		return
	}
	b, _ := json.Marshal(report)
	s.DB.Exec(ctx, "UPDATE youtube_takeout_import_jobs SET report_json = $2, import_completed = TRUE, updated_at = $3 WHERE id = $1", id, string(b), time.Now().UnixMilli())
	s.setTakeoutStatus(ctx, id, "completed", "completed", 100)
	if path, err := s.takeoutArchive(ctx, user, id); err == nil {
		os.Remove(path)
	}
}

// importTakeout is YoutubeTakeoutImporterService.commit: subscriptions, playlists (mapped by Takeout id across
// imports through youtube_takeout_playlist_keys), playlist items, then favorites, watch later and history.
func (s *Server) importTakeout(ctx context.Context, user, id string, p *portability.LegacyParsed, plan takeoutPlan) (map[string]any, error) {
	issues, summary := takeoutIssues(p.Warnings, p.Errors, "commit")
	var total, processed int64
	var items int
	for _, k := range p.ItemKeys {
		items += len(p.Items[k])
	}
	for _, n := range []struct {
		on bool
		n  int
	}{{plan.ImportSubscriptions, len(p.Subscriptions)}, {plan.ImportPlaylists, len(p.Playlists)}, {plan.ImportPlaylistItems, items},
		{plan.ImportFavorites, len(p.Favorites)}, {plan.ImportWatchLater, len(p.WatchLater)}, {plan.ImportHistory, len(p.History)}} {
		if n.on {
			total += int64(n.n)
		}
	}
	last := -1
	step := func() {
		processed++
		pct := 99
		if total > 0 {
			pct = min(int(float64(processed)/float64(total)*99), 99)
		}
		if pct > last {
			last = pct
			s.setTakeoutStatus(ctx, id, "running", "importing", pct)
		}
	}
	now := time.Now().UnixMilli()
	subs, err := s.stringSet(ctx, "SELECT channel_url FROM subscriptions WHERE user_id = $1", user)
	if err != nil {
		return nil, err
	}
	var subStats, plStats, itemStats takeoutStats
	if plan.ImportSubscriptions {
		for _, sub := range p.Subscriptions {
			c := db.CanonicalChannelURL(sub.ChannelURL)
			if subs[c] {
				subStats.Skipped++
			} else {
				err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
					if err := lockSubscriptions(ctx, tx, user); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, "INSERT INTO subscriptions (user_id, channel_url, name, avatar_url, subscribed_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING",
						user, c, sub.Name, sub.AvatarURL, time.Now().UnixMilli())
					return err
				})
				if err != nil {
					return nil, err
				}
				subs[c] = true
				subStats.Imported++
			}
			step()
		}
	}
	mappings := map[string]string{}
	rows, err := s.DB.Query(ctx, "SELECT source_key, playlist_id FROM youtube_takeout_playlist_keys WHERE user_id = $1", user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			mappings[k] = v
		}
	}
	rows.Close()
	existingByName := map[string]string{}
	rows, err = s.DB.Query(ctx, "SELECT id, name FROM playlists WHERE user_id = $1 ORDER BY created_at", user)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var pid, name string
		if rows.Scan(&pid, &name) == nil {
			if _, ok := existingByName[strings.ToLower(name)]; !ok {
				existingByName[strings.ToLower(name)] = pid
			}
		}
	}
	rows.Close()
	playlistExists := func(pid string) bool {
		var ok bool
		s.DB.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM playlists WHERE id = $1 AND user_id = $2)", pid, user).Scan(&ok)
		return ok
	}
	bySource := map[string]string{}
	if plan.ImportPlaylists {
		for _, pl := range p.Playlists {
			if isSystemList(pl.Name) || isSystemList(pl.ID) {
				plStats.Skipped++
				step()
				continue
			}
			nameKey, idKey := strings.ToLower(pl.Name), strings.ToLower(pl.ID)
			target := ""
			if m := mappings[idKey]; m != "" && playlistExists(m) {
				target = m
			} else if e := existingByName[nameKey]; e != "" {
				target = e
			}
			if target != "" {
				plStats.Skipped++
			} else {
				target = auth.UUID()
				if _, err := s.DB.Exec(ctx, "INSERT INTO playlists (id, user_id, name, description, created_at) VALUES ($1, $2, $3, $4, $5)",
					target, user, pl.Name, pl.Description, time.Now().UnixMilli()); err != nil {
					return nil, err
				}
				plStats.Imported++
			}
			bySource[nameKey] = target
			if idKey != "" {
				bySource[idKey] = target
				if _, err := s.DB.Exec(ctx, `INSERT INTO youtube_takeout_playlist_keys (user_id, source_key, playlist_id, updated_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, source_key) DO UPDATE SET playlist_id = EXCLUDED.playlist_id, updated_at = EXCLUDED.updated_at`,
					user, strings.ToLower(strings.TrimSpace(idKey)), target, time.Now().UnixMilli()); err != nil {
					return nil, err
				}
				mappings[idKey] = target
			}
			step()
		}
	}
	if plan.ImportPlaylistItems {
		for _, key := range p.ItemKeys {
			videos := p.Items[key]
			k := strings.ToLower(key)
			target := ""
			if isSystemList(k) {
				itemStats.Skipped += len(videos)
				for range videos {
					step()
				}
				continue
			}
			if m := mappings[k]; m != "" && playlistExists(m) {
				target = m
			} else {
				target = bySource[k]
			}
			if target == "" {
				itemStats.Skipped += len(videos)
				for range videos {
					step()
				}
				continue
			}
			have, err := s.stringSet(ctx, "SELECT url FROM playlist_videos WHERE user_id = $1 AND playlist_id = $2", user, target)
			if err != nil {
				return nil, err
			}
			for _, v := range videos {
				if have[v.URL] {
					itemStats.Skipped++
				} else {
					added := v.AddedAt
					if added <= 0 {
						added = time.Now().UnixMilli()
					}
					if _, err := s.DB.Exec(ctx, `INSERT INTO playlist_videos (id, playlist_id, user_id, url, title, thumbnail, duration, position, channel_name,
channel_url, channel_avatar, view_count, added_at, published_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, (SELECT count(*) FROM playlist_videos WHERE playlist_id = $2 AND user_id = $3), $8, $9, $10, $11, $12, $13)`,
						auth.UUID(), target, user, v.URL, v.Title, v.Thumbnail, v.Duration, v.ChannelName, v.ChannelURL, v.ChannelAvatar, v.ViewCount, added, v.PublishedAt); err != nil {
						return nil, err
					}
					have[v.URL] = true
					itemStats.Imported++
				}
				step()
			}
		}
	}
	var favStats, laterStats, histStats takeoutStats
	if plan.ImportFavorites {
		have, err := s.stringSet(ctx, "SELECT video_url FROM favorites WHERE user_id = $1", user)
		if err != nil {
			return nil, err
		}
		for _, f := range p.Favorites {
			if have[f.VideoURL] {
				favStats.Skipped++
			} else {
				at := f.FavoritedAt
				if at <= 0 {
					at = time.Now().UnixMilli()
				}
				if _, err := s.DB.Exec(ctx, `INSERT INTO favorites (user_id, video_url, favorited_at, title, thumbnail, duration, channel_name, channel_url,
channel_avatar, view_count, published_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`,
					user, f.VideoURL, at, firstNonEmpty(f.Title, ""), f.Thumbnail, f.Duration, f.ChannelName, f.ChannelURL, f.ChannelAvatar, f.ViewCount, f.PublishedAt); err != nil {
					return nil, err
				}
				have[f.VideoURL] = true
				favStats.Imported++
			}
			step()
		}
	}
	if plan.ImportWatchLater {
		have, err := s.stringSet(ctx, "SELECT url FROM watch_later WHERE user_id = $1", user)
		if err != nil {
			return nil, err
		}
		for _, v := range p.WatchLater {
			if have[v.URL] {
				laterStats.Skipped++
			} else {
				at := v.AddedAt
				if at <= 0 {
					at = time.Now().UnixMilli()
				}
				if _, err := s.DB.Exec(ctx, `INSERT INTO watch_later (user_id, url, title, thumbnail, duration, added_at, channel_name, channel_url, channel_avatar,
view_count, published_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`,
					user, v.URL, v.Title, v.Thumbnail, v.Duration, at, v.ChannelName, v.ChannelURL, v.ChannelAvatar, v.ViewCount, v.PublishedAt); err != nil {
					return nil, err
				}
				have[v.URL] = true
				laterStats.Imported++
			}
			step()
		}
	}
	if plan.ImportHistory {
		have, err := s.stringSet(ctx, "SELECT url || '|' || watched_at FROM history WHERE user_id = $1", user)
		if err != nil {
			return nil, err
		}
		var batch pgx.Batch
		for _, h := range p.History {
			key := h.URL + "|" + strconv.FormatInt(h.WatchedAt, 10)
			if have[key] {
				histStats.Skipped++
			} else {
				have[key] = true
				at := h.WatchedAt
				if at <= 0 {
					at = now
				}
				// Saved progress, in milliseconds, wins over the export's zero, as HistoryProgressMapper does.
				batch.Queue(`INSERT INTO history (id, user_id, url, title, thumbnail, channel_name, channel_url, channel_avatar, duration, progress, watched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE((SELECT position / 1000 FROM progress WHERE user_id = $2 AND video_url = $3), $10), $11)`,
					auth.UUID(), user, h.URL, h.Title, h.Thumbnail, h.ChannelName, h.ChannelURL, h.ChannelAvatar, h.Duration, h.Progress, at)
				histStats.Imported++
			}
			step()
		}
		if batch.Len() > 0 {
			if err := s.DB.SendBatch(ctx, &batch).Close(); err != nil {
				return nil, err
			}
		}
	}
	if plan.ImportSubscriptions {
		s.feeds.invalidate(user)
	}
	return map[string]any{
		"subscriptions": subStats, "playlists": plStats, "playlistItems": itemStats, "favorites": favStats, "watchLater": laterStats, "history": histStats,
		"skippedItems": takeoutCounts{subStats.Skipped, plStats.Skipped, itemStats.Skipped, favStats.Skipped, laterStats.Skipped, histStats.Skipped},
		"warnings":     p.Warnings, "errors": p.Errors, "issues": issues, "issueSummary": summary, "finishedAt": time.Now().UnixMilli(),
	}, nil
}

func isSystemList(v string) bool { return portability.SystemPlaylist(v) != "" }

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
