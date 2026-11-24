package api

import (
	"context"
	"strconv"
	"strings"

	"typetype.dev/server/internal/auth"

	"github.com/jackc/pgx/v5"
)

// backupTable maps one per-user table to its backup items: every column becomes the camelCase field the Kotlin
// item uses, so export and restore read and write the same JSON as TypeTypeBackupService.
type backupTable struct {
	table, order string
	cols         []string
	newID        bool
	where        string
}

var backupTables = map[string]backupTable{
	"history": {table: "history", order: "watched_at DESC", newID: true,
		cols: []string{"id", "url", "title", "thumbnail", "channel_name", "channel_url", "channel_avatar", "duration", "progress", "watched_at"}},
	"watchLater": {table: "watch_later", order: "added_at DESC",
		cols: []string{"url", "title", "thumbnail", "duration", "added_at", "channel_name", "channel_url", "channel_avatar", "view_count", "published_at"}},
	"favorites": {table: "favorites", order: "favorited_at DESC",
		cols: []string{"video_url", "favorited_at", "title", "thumbnail", "duration", "channel_name", "channel_url", "channel_avatar", "view_count", "published_at"}},
	"progress":      {table: "progress", order: "updated_at DESC", cols: []string{"video_url", "position", "updated_at"}},
	"searchHistory": {table: "search_history", order: "searched_at DESC", newID: true, cols: []string{"id", "term", "searched_at"}},
	"savedPlaylists": {table: "saved_playlists", order: "saved_at DESC", newID: true,
		cols: []string{"id", "public_playlist_id", "url", "title", "thumbnail_url", "uploader_name", "stream_count", "playlist_type", "saved_at"}},
	"subscriptions": {table: "subscriptions", order: "subscribed_at DESC", cols: []string{"channel_url", "name", "avatar_url", "subscribed_at"}},
	"blockedChannels": {table: "blocked_channels", order: "blocked_at DESC", where: "scope = 'user'",
		cols: []string{"channel_url AS url", "name", "thumbnail_url", "blocked_at"}},
	"blockedVideos":   {table: "blocked_videos", order: "blocked_at DESC", where: "scope = 'user'", cols: []string{"video_url AS url", "blocked_at"}},
	"blockedKeywords": {table: "blocked_keywords", order: "blocked_at DESC", where: "scope = 'user'", cols: []string{"keyword", "blocked_at"}},
	"allowedChannels": {table: "allowed_channels", order: "allowed_at DESC", where: "scope = 'user'",
		cols: []string{"channel_url AS url", "name", "thumbnail_url", "allowed_at"}},
	"allowedPlaylists": {table: "allowed_playlists", order: "allowed_at DESC", where: "scope = 'user'",
		cols: []string{"playlist_url AS url", "title", "thumbnail_url", "uploader_name", "allowed_at"}},
}

func camel(col string) string {
	if _, alias, ok := strings.Cut(col, " AS "); ok {
		col = alias
	}
	parts := strings.Split(col, "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func column(col string) string {
	c, _, _ := strings.Cut(col, " AS ")
	return c
}

func (s *Server) exportTable(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, user string, t backupTable) ([]map[string]any, error) {
	where := "user_id = $1"
	if t.where != "" {
		where += " AND " + t.where
	}
	rows, err := q.Query(ctx, "SELECT "+strings.Join(t.cols, ", ")+" FROM "+t.table+" WHERE "+where+" ORDER BY "+t.order, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		item := map[string]any{}
		for i, c := range t.cols {
			item[camel(c)] = vals[i]
		}
		if strings.HasPrefix(t.table, "blocked_") || strings.HasPrefix(t.table, "allowed_") {
			item["global"] = false
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// restoreTable replaces the user's rows of a table with the backup items, as the Kotlin writer's delete then insert.
func restoreTable(ctx context.Context, tx pgx.Tx, user string, t backupTable, items []map[string]any, fixed []fixedCol,
	adjust func(map[string]any)) (int, error) {
	where := "user_id = $1"
	if t.where != "" {
		where += " AND " + t.where
	}
	if t.where != "FALSE" {
		if _, err := tx.Exec(ctx, "DELETE FROM "+t.table+" WHERE "+where, user); err != nil {
			return 0, err
		}
	}
	cols := []string{"user_id"}
	for _, c := range t.cols {
		cols = append(cols, column(c))
	}
	for _, f := range fixed {
		cols = append(cols, f.col)
	}
	marks := make([]string, len(cols))
	for i := range cols {
		marks[i] = "$" + strconv.Itoa(i+1)
	}
	stmt := "INSERT INTO " + t.table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
	for _, it := range items {
		if adjust != nil {
			adjust(it)
		}
		args := []any{user}
		for _, c := range t.cols {
			v := it[camel(c)]
			if column(c) == "id" && t.newID {
				v = auth.UUID()
			}
			args = append(args, jsonValue(v))
		}
		for _, f := range fixed {
			args = append(args, f.val)
		}
		if _, err := tx.Exec(ctx, stmt, args...); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

// jsonValue turns JSON numbers that are whole into int64 so BIGINT columns accept them.
func jsonValue(v any) any {
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return int64(f)
	}
	return v
}

type fixedCol struct {
	col string
	val any
}
