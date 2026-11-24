package api

import (
	"context"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/store"
)

// restoreBackup is TypeTypeBackupRestoreWriter: each selected category replaces the user's data, in one transaction.
func (s *Server) restoreBackup(ctx context.Context, user string, b *backupFile, cats map[string]bool) (map[string]int, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	out := map[string]int{}
	put := func(key string, n int, err error) error {
		out[key] = n
		return err
	}
	if cats["subscriptions"] {
		canon := func(it map[string]any) {
			if u, ok := it["channelUrl"].(string); ok {
				it["channelUrl"] = db.CanonicalChannelURL(u)
			}
		}
		n, err := restoreTable(ctx, tx, user, backupTables["subscriptions"], b.Subscriptions, nil, canon)
		if err := put("subscriptions", n, err); err != nil {
			return nil, err
		}
		urls := make([]string, 0, len(b.Subscriptions))
		for _, it := range b.Subscriptions {
			u, _ := it["channelUrl"].(string)
			urls = append(urls, u)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM subscription_group_memberships WHERE user_id = $1 AND NOT (channel_url = ANY($2))", user, urls); err != nil {
			return nil, err
		}
		if b.SubscriptionGroups != nil {
			tx.Exec(ctx, "DELETE FROM subscription_group_memberships WHERE user_id = $1", user)
			tx.Exec(ctx, "DELETE FROM subscription_groups WHERE user_id = $1", user)
			members := 0
			now := time.Now().UnixMilli()
			for _, g := range b.SubscriptionGroups {
				id := auth.UUID()
				name, _ := g["name"].(string)
				if _, err := tx.Exec(ctx, `INSERT INTO subscription_groups (id, user_id, name, normalized_name, created_at, updated_at) VALUES ($1, $2, $3, lower($3), $4, $5)`,
					id, user, name, jsonValue(g["createdAt"]), jsonValue(g["updatedAt"])); err != nil {
					return nil, err
				}
				chans, _ := g["channelUrls"].([]any)
				for _, x := range chans {
					u, _ := x.(string)
					if _, err := tx.Exec(ctx, "INSERT INTO subscription_group_memberships (group_id, user_id, channel_url, added_at) VALUES ($1, $2, $3, $4)",
						id, user, db.CanonicalChannelURL(u), now); err != nil {
						return nil, err
					}
					members++
				}
			}
			out["subscriptionGroups"], out["subscriptionGroupMemberships"] = len(b.SubscriptionGroups), members
		}
	}
	for _, c := range []struct {
		key   string
		items []map[string]any
	}{{"history", b.History}, {"watchLater", b.WatchLater}, {"favorites", b.Favorites}, {"progress", b.Progress},
		{"searchHistory", b.SearchHistory}, {"savedPlaylists", b.SavedPlaylists}} {
		if !cats[c.key] {
			continue
		}
		var adjust func(map[string]any)
		if c.key == "progress" {
			adjust = func(it map[string]any) {
				if p, ok := it["position"].(float64); ok && p < 0 {
					it["position"] = 0.0
				}
			}
		}
		n, err := restoreTable(ctx, tx, user, backupTables[c.key], c.items, nil, adjust)
		if err := put(c.key, n, err); err != nil {
			return nil, err
		}
	}
	if cats["playlists"] {
		tx.Exec(ctx, "DELETE FROM playlist_videos WHERE user_id = $1", user)
		tx.Exec(ctx, "DELETE FROM playlists WHERE user_id = $1", user)
		videos := 0
		vt := backupTable{table: "playlist_videos", newID: true, where: "FALSE", cols: []string{"id", "url", "title", "thumbnail", "duration", "position",
			"channel_name", "channel_url", "channel_avatar", "view_count", "added_at", "published_at"}}
		for _, p := range b.Playlists {
			id := auth.UUID()
			if _, err := tx.Exec(ctx, "INSERT INTO playlists (id, user_id, name, description, created_at) VALUES ($1, $2, $3, $4, $5)",
				id, user, p["name"], p["description"], jsonValue(p["createdAt"])); err != nil {
				return nil, err
			}
			var items []map[string]any
			raw, _ := p["videos"].([]any)
			for _, x := range raw {
				if m, ok := x.(map[string]any); ok {
					items = append(items, m)
				}
			}
			n, err := restoreTable(ctx, tx, user, vt, items, []fixedCol{{"playlist_id", id}}, nil)
			if err != nil {
				return nil, err
			}
			videos += n
		}
		out["playlists"], out["playlistVideos"] = len(b.Playlists), videos
	}
	if cats["contentFilters"] {
		for _, k := range []string{"blockedChannels", "blockedVideos", "blockedKeywords", "allowedChannels", "allowedPlaylists"} {
			var adjust func(map[string]any)
			switch k {
			case "blockedKeywords":
				adjust = func(it map[string]any) { kw, _ := it["keyword"].(string); it["keyword"] = normalizeKeyword(kw) }
			case "allowedChannels":
				adjust = func(it map[string]any) { u, _ := it["url"].(string); it["url"] = channelKey(u) }
			case "allowedPlaylists":
				adjust = func(it map[string]any) { u, _ := it["url"].(string); it["url"] = playlistKey(u) }
			}
			n, err := restoreTable(ctx, tx, user, backupTables[k], b.ContentFilters[k], []fixedCol{{"scope", "user"}}, adjust)
			if err := put(k, n, err); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if cats["settings"] {
		st := *b.Settings
		st.Normalize()
		if _, err := store.UpsertSettings(ctx, s.DB, user, st); err != nil {
			return nil, err
		}
		out["settings"] = 1
	}
	return out, nil
}
