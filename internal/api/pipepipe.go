package api

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/store"
)

type ppHistory struct {
	at                                       int64
	url, title, uploader, uploaderURL, thumb string
	duration                                 int64
}

type ppVideo struct {
	url, title, thumb string
	duration          int64
	position          int
}

type ppSnapshot struct {
	subs      [][3]string
	history   []ppHistory
	playlists []struct {
		name   string
		videos []ppVideo
	}
	progress map[string]int64
	searches []struct {
		term string
		at   int64
	}
}

// extractPipePipeDB pulls newpipe.db (or pipepipe.db) out of a PipePipe or NewPipe export with the Kotlin limits:
// 512 entries, a 128 MB database and a 120:1 compression ratio at most.
func extractPipePipeDB(archive []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return "", err
	}
	var files []*zip.File
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() {
			files = append(files, f)
		}
	}
	if len(files) > 512 {
		return "", errors.New("too many entries")
	}
	var dbFile *zip.File
	for _, f := range files {
		name := strings.ToLower(filepath.Base(f.Name))
		if name == "newpipe.db" || name == "pipepipe.db" {
			if dbFile != nil {
				return "", errors.New("several databases")
			}
			dbFile = f
		}
	}
	if dbFile == nil {
		return "", errors.New("missing database")
	}
	if dbFile.UncompressedSize64 == 0 || dbFile.UncompressedSize64 > 128<<20 ||
		dbFile.CompressedSize64 > 0 && float64(dbFile.UncompressedSize64)/float64(dbFile.CompressedSize64) > 120 {
		return "", errors.New("invalid database size")
	}
	rc, err := dbFile.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	tmp, err := os.CreateTemp("", "pipepipe-db-*.sqlite")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(tmp, io.LimitReader(rc, 128<<20+1))
	tmp.Close()
	if err != nil || n <= 0 || n > 128<<20 {
		os.Remove(tmp.Name())
		return "", errors.New("invalid database")
	}
	return tmp.Name(), nil
}

func hasTable(d *sql.DB, name string) bool {
	var n int
	d.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n)
	return n > 0
}

func hasColumn(d *sql.DB, table, col string) bool {
	rows, err := d.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil && c == col {
			return true
		}
	}
	return false
}

func readPipePipe(path string) (*ppSnapshot, error) {
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	snap := &ppSnapshot{progress: map[string]int64{}}
	str := func(v sql.NullString) string { return v.String }
	if hasTable(d, "subscriptions") {
		rows, err := d.Query("SELECT url, name, avatar_url FROM subscriptions")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var u, n, a sql.NullString
			if rows.Scan(&u, &n, &a) == nil {
				snap.subs = append(snap.subs, [3]string{str(u), str(n), str(a)})
			}
		}
		rows.Close()
	}
	histSQL := ""
	switch {
	case hasTable(d, "stream_history"):
		histSQL = `SELECT sh.access_date, s.url, s.title, s.duration, s.uploader, s.uploader_url, s.thumbnail_url FROM stream_history sh
INNER JOIN streams s ON s.uid = sh.stream_id ORDER BY sh.access_date DESC`
	case hasColumn(d, "streams", "last_access_date"):
		histSQL = `SELECT last_access_date, url, title, duration, uploader, uploader_url, thumbnail_url FROM streams
WHERE COALESCE(last_access_date, 0) > 0 ORDER BY last_access_date DESC`
	}
	if histSQL != "" {
		rows, err := d.Query(histSQL)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var at, dur sql.NullInt64
			var u, t, up, upu, th sql.NullString
			if rows.Scan(&at, &u, &t, &dur, &up, &upu, &th) == nil {
				snap.history = append(snap.history, ppHistory{at.Int64, str(u), str(t), str(up), str(upu), str(th), dur.Int64})
			}
		}
		rows.Close()
	}
	if hasTable(d, "playlists") && hasTable(d, "playlist_stream_join") {
		rows, err := d.Query("SELECT uid, name FROM playlists ORDER BY display_index ASC, uid ASC")
		if err != nil {
			rows, err = d.Query("SELECT uid, name FROM playlists ORDER BY uid ASC")
		}
		if err != nil {
			return nil, err
		}
		index := map[int64]int{}
		for rows.Next() {
			var id int64
			var n sql.NullString
			if rows.Scan(&id, &n) == nil {
				name := strings.TrimSpace(n.String)
				if name == "" {
					name = "Playlist"
				}
				index[id] = len(snap.playlists)
				snap.playlists = append(snap.playlists, struct {
					name   string
					videos []ppVideo
				}{name: name})
			}
		}
		rows.Close()
		vrows, err := d.Query(`SELECT ps.playlist_id, ps.join_index, s.url, s.title, s.duration, s.thumbnail_url FROM playlist_stream_join ps
INNER JOIN streams s ON s.uid = ps.stream_id ORDER BY ps.playlist_id ASC, ps.join_index ASC`)
		if err != nil {
			return nil, err
		}
		for vrows.Next() {
			var pid, pos, dur sql.NullInt64
			var u, t, th sql.NullString
			if vrows.Scan(&pid, &pos, &u, &t, &dur, &th) == nil {
				if i, ok := index[pid.Int64]; ok {
					snap.playlists[i].videos = append(snap.playlists[i].videos, ppVideo{str(u), str(t), str(th), dur.Int64, int(pos.Int64)})
				}
			}
		}
		vrows.Close()
	}
	addProgress := func(q string) {
		rows, err := d.Query(q)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var u sql.NullString
			var p sql.NullInt64
			if rows.Scan(&u, &p) == nil && u.String != "" && p.Int64 > snap.progress[u.String] {
				snap.progress[u.String] = p.Int64
			}
		}
	}
	if hasTable(d, "stream_state") {
		addProgress("SELECT s.url, ss.progress_time FROM stream_state ss INNER JOIN streams s ON s.uid = ss.stream_id")
	}
	if hasColumn(d, "streams", "progress_time") {
		addProgress("SELECT url, progress_time FROM streams WHERE COALESCE(progress_time, 0) > 0")
	}
	if hasTable(d, "search_history") {
		rows, err := d.Query("SELECT search, creation_date FROM search_history ORDER BY creation_date DESC")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t sql.NullString
			var at sql.NullInt64
			if rows.Scan(&t, &at) == nil {
				snap.searches = append(snap.searches, struct {
					term string
					at   int64
				}{t.String, at.Int64})
			}
		}
		rows.Close()
	}
	return snap, nil
}

// normalizeTimeline spreads the watch history over the last thirty days, keeping its order, as
// PipePipeBackupTimelineNormalizer does for the "normalized" time mode.
func normalizeTimeline(h []ppHistory) {
	if len(h) == 0 {
		return
	}
	sort.SliceStable(h, func(i, j int) bool { return h[i].at < h[j].at })
	oldMin, oldMax, now := h[0].at, h[len(h)-1].at, time.Now().UnixMilli()
	if oldMax <= oldMin {
		for i := range h {
			h[i].at = now - int64(len(h)-i)
		}
	} else {
		newMin := now - 30*24*3600*1000
		scale := float64(now-newMin) / float64(oldMax-oldMin)
		for i := range h {
			h[i].at = newMin + int64(float64(h[i].at-oldMin)*scale)
		}
	}
	sort.SliceStable(h, func(i, j int) bool { return h[i].at > h[j].at })
}

func (s *Server) persistPipePipe(ctx context.Context, user string, snap *ppSnapshot) (map[string]int, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	for _, t := range []string{"history", "subscriptions", "playlist_videos", "playlists", "progress", "search_history"} {
		if _, err := tx.Exec(ctx, "DELETE FROM "+t+" WHERE user_id = $1", user); err != nil {
			return nil, err
		}
	}
	counts := map[string]int{}
	ins := func(key, q string, args ...any) error {
		tag, err := tx.Exec(ctx, q+" ON CONFLICT DO NOTHING", args...)
		if err == nil {
			counts[key] += int(tag.RowsAffected())
		}
		return err
	}
	avatars := map[string]string{}
	for _, sub := range snap.subs {
		if sub[0] != "" {
			avatars[sub[0]] = sub[2]
		}
	}
	now := time.Now().UnixMilli()
	for _, h := range snap.history {
		if err := ins("history", `INSERT INTO history (id, user_id, url, title, thumbnail, channel_name, channel_url, channel_avatar, duration, progress, watched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 0, $10)`, auth.UUID(), user, h.url, store.FallbackTitle(h.title, h.url), store.FallbackThumbnail(h.thumb, h.url),
			h.uploader, h.uploaderURL, avatars[h.uploaderURL], h.duration, h.at); err != nil {
			return nil, err
		}
	}
	urls := []string{}
	for _, sub := range snap.subs {
		c := db.CanonicalChannelURL(sub[0])
		urls = append(urls, c)
		if err := ins("subscriptions", "INSERT INTO subscriptions (user_id, channel_url, name, avatar_url, subscribed_at) VALUES ($1, $2, $3, $4, $5)",
			user, c, sub[1], sub[2], now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM subscription_group_memberships WHERE user_id = $1 AND NOT (channel_url = ANY($2))", user, urls); err != nil {
		return nil, err
	}
	for _, p := range snap.playlists {
		id := auth.UUID()
		if err := ins("playlists", "INSERT INTO playlists (id, user_id, name, description, created_at) VALUES ($1, $2, $3, '', $4)", id, user, p.name, now); err != nil {
			return nil, err
		}
		sort.SliceStable(p.videos, func(i, j int) bool { return p.videos[i].position < p.videos[j].position })
		for i, v := range p.videos {
			if err := ins("playlistVideos", `INSERT INTO playlist_videos (id, playlist_id, user_id, url, title, thumbnail, duration, position) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				auth.UUID(), id, user, v.url, store.FallbackTitle(v.title, v.url), store.FallbackThumbnail(v.thumb, v.url), v.duration, i); err != nil {
				return nil, err
			}
		}
	}
	for u, pos := range snap.progress {
		if err := ins("progress", "INSERT INTO progress (user_id, video_url, position, updated_at) VALUES ($1, $2, $3, $4)", user, u, pos, now); err != nil {
			return nil, err
		}
	}
	for _, sh := range snap.searches {
		if err := ins("searchHistory", "INSERT INTO search_history (id, user_id, term, searched_at) VALUES ($1, $2, $3, $4)", auth.UUID(), user, sh.term, sh.at); err != nil {
			return nil, err
		}
	}
	return counts, tx.Commit(ctx)
}

func (s *Server) pipePipeRoutes(rt *Router) {
	rt.Handle("POST", "/restore/pipepipe", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		mode := "raw"
		if r.URL.Query().Has("timeMode") {
			mode = strings.ToLower(r.URL.Query().Get("timeMode"))
			if mode != "raw" && mode != "normalized" {
				plain(w, r, http.StatusBadRequest, "Invalid timeMode")
				return
			}
		}
		mr, err := r.MultipartReader()
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Missing file part")
			return
		}
		var archive []byte
		files := 0
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				plain(w, r, http.StatusBadRequest, "Invalid backup archive")
				return
			}
			if p.FormName() == "file" && p.FileName() != "" {
				ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
				if !strings.HasSuffix(strings.ToLower(p.FileName()), ".zip") && ct != "application/zip" && ct != "application/octet-stream" {
					plain(w, r, http.StatusBadRequest, "Invalid backup file type")
					return
				}
				b, err := io.ReadAll(io.LimitReader(p, 32<<20+1))
				if err != nil || len(b) > 32<<20 {
					plain(w, r, http.StatusBadRequest, "Invalid backup archive")
					return
				}
				archive, files = b, files+1
			}
			p.Close()
		}
		if files > 1 {
			plain(w, r, http.StatusBadRequest, "Only one backup file is allowed")
			return
		}
		if files == 0 {
			plain(w, r, http.StatusBadRequest, "Missing file part")
			return
		}
		path, err := extractPipePipeDB(archive)
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Invalid backup archive")
			return
		}
		defer os.Remove(path)
		snap, err := readPipePipe(path)
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Invalid backup archive")
			return
		}
		if mode == "normalized" {
			normalizeTimeline(snap.history)
		}
		counts, err := s.persistPipePipe(r.Context(), user, snap)
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Invalid backup archive")
			return
		}
		var lo, hi int64
		for i, h := range snap.history {
			if i == 0 || h.at < lo {
				lo = h.at
			}
			if h.at > hi {
				hi = h.at
			}
		}
		s.feedChanged(user)
		s.recs.invalidate(r.Context(), user)
		httpx.JSON(w, http.StatusOK, map[string]any{"history": counts["history"], "subscriptions": counts["subscriptions"], "playlists": counts["playlists"],
			"playlistVideos": counts["playlistVideos"], "progress": counts["progress"], "searchHistory": counts["searchHistory"], "timeMode": mode,
			"historyMinWatchedAt": lo, "historyMaxWatchedAt": hi})
	}))
}
