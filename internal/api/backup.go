package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/store"
)

var backupCategories = []string{"subscriptions", "history", "playlists", "watchLater", "favorites", "progress", "searchHistory", "savedPlaylists",
	"settings", "contentFilters"}

func parseBackupCategories(raw string) map[string]bool {
	out := map[string]bool{}
	if strings.TrimSpace(raw) == "" || raw == "all" {
		for _, c := range backupCategories {
			out[c] = true
		}
		return out
	}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !contains(backupCategories, p) {
			return nil
		}
		out[p] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Server) exportBackup(ctx context.Context, user string, cats map[string]bool) (map[string]any, error) {
	names := make([]string, 0, len(cats))
	for c := range cats {
		names = append(names, c)
	}
	sort.Strings(names)
	out := map[string]any{"format": "typetype-backup", "version": 1, "exportedAt": time.Now().UnixMilli(), "categories": names}
	for _, c := range []string{"history", "watchLater", "favorites", "progress", "searchHistory", "savedPlaylists"} {
		if cats[c] {
			items, err := s.exportTable(ctx, s.DB, user, backupTables[c])
			if err != nil {
				return nil, err
			}
			out[c] = items
		}
	}
	if cats["subscriptions"] {
		subs, err := s.exportTable(ctx, s.DB, user, backupTables["subscriptions"])
		if err != nil {
			return nil, err
		}
		out["subscriptions"] = subs
		known := map[string]bool{}
		for _, it := range subs {
			if u, ok := it["channelUrl"].(string); ok {
				known[db.CanonicalChannelURL(u)] = true
			}
		}
		groups := []map[string]any{}
		rows, err := s.DB.Query(ctx, `SELECT g.id, g.name, g.created_at, g.updated_at, COALESCE(array_agg(m.channel_url ORDER BY m.added_at)
FILTER (WHERE m.channel_url IS NOT NULL), '{}') FROM subscription_groups g LEFT JOIN subscription_group_memberships m ON m.group_id = g.id AND m.user_id = g.user_id
WHERE g.user_id = $1 GROUP BY g.id ORDER BY g.created_at`, user)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name string
			var created, updated int64
			var urls []string
			if rows.Scan(&id, &name, &created, &updated, &urls) != nil {
				continue
			}
			kept := []string{}
			for _, u := range urls {
				if known[db.CanonicalChannelURL(u)] {
					kept = append(kept, u)
				}
			}
			groups = append(groups, map[string]any{"name": name, "channelUrls": kept, "createdAt": created, "updatedAt": updated})
		}
		rows.Close()
		out["subscriptionGroups"] = groups
	}
	if cats["playlists"] {
		pls := []map[string]any{}
		rows, err := s.DB.Query(ctx, "SELECT id, name, description, created_at FROM playlists WHERE user_id = $1 ORDER BY created_at", user)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name, desc string
			var created int64
			if rows.Scan(&id, &name, &desc, &created) == nil {
				pls = append(pls, map[string]any{"id": id, "name": name, "description": desc, "createdAt": created})
			}
		}
		rows.Close()
		for _, p := range pls {
			vids := backupTable{table: "playlist_videos", order: "position", cols: []string{"id", "url", "title", "thumbnail", "duration", "position", "channel_name",
				"channel_url", "channel_avatar", "view_count", "added_at", "published_at"}, where: "playlist_id = '" + strings.ReplaceAll(p["id"].(string), "'", "") + "'"}
			items, err := s.exportTable(ctx, s.DB, user, vids)
			if err != nil {
				return nil, err
			}
			for _, v := range items {
				v["watchPosition"], v["watched"], v["progressUpdatedAt"] = 0, false, 0
			}
			p["videos"], p["videoCount"] = items, len(items)
		}
		out["playlists"] = pls
	}
	if cats["settings"] {
		st, err := store.GetSettings(ctx, s.DB, user)
		if err != nil {
			return nil, err
		}
		out["settings"] = st
	}
	if cats["contentFilters"] {
		cf := map[string]any{}
		for _, k := range []string{"blockedChannels", "blockedVideos", "blockedKeywords", "allowedChannels", "allowedPlaylists"} {
			items, err := s.exportTable(ctx, s.DB, user, backupTables[k])
			if err != nil {
				return nil, err
			}
			cf[k] = items
		}
		out["contentFilters"] = cf
	}
	return out, nil
}

type backupFile struct {
	Format             string                      `json:"format"`
	Version            int                         `json:"version"`
	Categories         []string                    `json:"categories"`
	Subscriptions      []map[string]any            `json:"subscriptions"`
	SubscriptionGroups []map[string]any            `json:"subscriptionGroups"`
	History            []map[string]any            `json:"history"`
	Playlists          []map[string]any            `json:"playlists"`
	WatchLater         []map[string]any            `json:"watchLater"`
	Favorites          []map[string]any            `json:"favorites"`
	Progress           []map[string]any            `json:"progress"`
	SearchHistory      []map[string]any            `json:"searchHistory"`
	SavedPlaylists     []map[string]any            `json:"savedPlaylists"`
	Settings           *store.Settings             `json:"settings"`
	ContentFilters     map[string][]map[string]any `json:"contentFilters"`
}

func (b *backupFile) validate() (map[string]bool, error) {
	if b.Format != "typetype-backup" {
		return nil, errors.New("Unsupported backup format")
	}
	if b.Version != 1 {
		return nil, errors.New("Unsupported backup version")
	}
	cats := parseBackupCategories(strings.Join(b.Categories, ","))
	if len(b.Categories) == 0 || cats == nil {
		return nil, errors.New("Invalid backup categories")
	}
	present := map[string]bool{"subscriptions": b.Subscriptions != nil, "history": b.History != nil, "playlists": b.Playlists != nil,
		"watchLater": b.WatchLater != nil, "favorites": b.Favorites != nil, "progress": b.Progress != nil, "searchHistory": b.SearchHistory != nil,
		"savedPlaylists": b.SavedPlaylists != nil, "settings": b.Settings != nil, "contentFilters": b.ContentFilters != nil}
	for c := range cats {
		if !present[c] {
			return nil, errors.New("Backup is missing selected data")
		}
	}
	if b.SubscriptionGroups != nil {
		if !cats["subscriptions"] {
			return nil, errors.New("Subscription groups require the subscriptions category")
		}
		subs := map[string]bool{}
		for _, it := range b.Subscriptions {
			u, _ := it["channelUrl"].(string)
			subs[db.CanonicalChannelURL(u)] = true
		}
		names := map[string]bool{}
		for _, g := range b.SubscriptionGroups {
			name, _ := g["name"].(string)
			if name != strings.TrimSpace(name) || len([]rune(name)) < 1 || len([]rune(name)) > 100 {
				return nil, errors.New("Subscription group names must contain 1 to 100 characters")
			}
			if names[strings.ToLower(name)] {
				return nil, errors.New("Backup contains duplicate subscription group names")
			}
			names[strings.ToLower(name)] = true
			seen := map[string]bool{}
			urls, _ := g["channelUrls"].([]any)
			for _, x := range urls {
				u, _ := x.(string)
				c := db.CanonicalChannelURL(u)
				if seen[c] {
					return nil, errors.New("Backup contains duplicate subscription group memberships")
				}
				if !subs[c] {
					return nil, errors.New("Subscription group membership references an unknown subscription")
				}
				seen[c] = true
			}
		}
	}
	if cats["contentFilters"] {
		seen := map[string]bool{}
		for _, k := range b.ContentFilters["blockedKeywords"] {
			kw, _ := k["keyword"].(string)
			n := normalizeKeyword(kw)
			if l := len([]rune(n)); l < 1 || l > 100 {
				return nil, errors.New("Blocked keywords must contain 1 to 100 characters")
			}
			if seen[n] {
				return nil, errors.New("Backup contains duplicate blocked keywords")
			}
			seen[n] = true
		}
	}
	return cats, nil
}

func (s *Server) backupRoutes(rt *Router) {
	rt.Handle("GET", "/backup/typetype", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		cats := parseBackupCategories(r.URL.Query().Get("categories"))
		if cats == nil {
			plain(w, r, http.StatusBadRequest, "Invalid backup categories")
			return
		}
		out, err := s.exportBackup(r.Context(), user, cats)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename=typetype-backup-`+time.Now().UTC().Format("2006-01-02")+`.json`)
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("POST", "/restore/typetype", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		body, n, err := uploadedFile(r, "file", 128<<20)
		if err != nil || n != 1 {
			plain(w, r, http.StatusBadRequest, "Exactly one backup file is required")
			return
		}
		var b backupFile
		if json.Unmarshal(body, &b) != nil {
			plain(w, r, http.StatusBadRequest, "Invalid TypeType backup")
			return
		}
		cats, err := b.validate()
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Invalid TypeType backup")
			return
		}
		restored, err := s.restoreBackup(r.Context(), user, &b, cats)
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Invalid TypeType backup")
			return
		}
		s.feedChanged(user)
		s.recs.invalidate(r.Context(), user)
		httpx.JSON(w, http.StatusOK, map[string]any{"restored": restored})
	}))
}

// uploadedFile reads the multipart parts named field, refusing more than limit bytes, and returns the last one with
// how many there were.
func uploadedFile(r *http.Request, field string, limit int64) ([]byte, int, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, 0, err
	}
	var data []byte
	n := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if p.FormName() == field && p.FileName() != "" {
			b, err := io.ReadAll(io.LimitReader(p, limit+1))
			if err != nil || int64(len(b)) > limit {
				return nil, 0, errors.New("file too large")
			}
			data, n = b, n+1
		}
		p.Close()
	}
	return data, n, nil
}
