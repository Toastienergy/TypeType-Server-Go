package portability

import (
	"archive/zip"
	"bufio"
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// newPipeTarget is NewPipeArchiveTarget: the Room schema version and identity of the database each app expects.
type newPipeTarget struct {
	version  int
	identity string
	pipePipe bool
	name     string
}

var (
	newPipeDB  = newPipeTarget{9, "7591e8039faa74d8c0517dc867af9d3e", false, "NEW PIPE"}
	pipePipeDB = newPipeTarget{901, "d505dd6c0be6a80da07aa980bd361064", true, "PIPE PIPE"}
)

const pipePipeVersion = 900

// newPipeAdapter is NewPipePortabilityAdapter (an archive below version 900, or the subscriptions JSON export)
// and PipePipePortabilityAdapter (an archive from version 900).
type newPipeAdapter struct{ target newPipeTarget }

func archiveCaps() []Capability {
	var out []Capability
	for _, c := range []Category{Subscriptions, SubscriptionGroups, History, Playlists, Progress, SearchHistory, SavedPlaylists} {
		f := Complete
		if c == Playlists {
			f = Partial
		}
		out = append(out, both(c, f))
	}
	return out
}

func (a newPipeAdapter) Descriptor() Descriptor {
	if a.target.pipePipe {
		return Descriptor{PipePipe, 2, archiveCaps(), "zip", "application/zip"}
	}
	return Descriptor{NewPipe, 3, archiveCaps(), "zip", "application/zip"}
}

func (newPipeAdapter) AutoDetect() bool { return true }

// databaseEntry is the single newpipe.db or pipepipe.db in the archive.
func databaseEntry(in *Input) string {
	if in.Archive == nil {
		return ""
	}
	found := ""
	for _, e := range in.Archive.Entries {
		if n := strings.ToLower(path.Base(e.Name)); n == "newpipe.db" || n == "pipepipe.db" {
			if found != "" {
				return ""
			}
			found = e.Name
		}
	}
	return found
}

// userVersion reads the SQLite header's user_version, big-endian at offset 60.
func userVersion(in *Input) (int, bool) {
	name := databaseEntry(in)
	if name == "" {
		return 0, false
	}
	z, err := zip.OpenReader(in.Path)
	if err != nil {
		return 0, false
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return 0, false
		}
		h := make([]byte, 64)
		n, _ := io.ReadFull(r, h)
		r.Close()
		if n < 64 || !bytes.HasPrefix(h, []byte("SQLite format 3\x00")) {
			return 0, false
		}
		return int(int32(binary.BigEndian.Uint32(h[60:64]))), true
	}
	return 0, false
}

func (a newPipeAdapter) Detect(in *Input) *Detection {
	if in.Archive != nil {
		v, ok := userVersion(in)
		if !ok {
			return nil
		}
		s := strconv.Itoa(v)
		if a.target.pipePipe {
			if v < pipePipeVersion {
				return nil
			}
			return &Detection{PipePipe, &s, 100, "PipePipe SQLite schema version"}
		}
		if v < 1 || v >= pipePipeVersion {
			return nil
		}
		return &Detection{NewPipe, &s, 100, "NewPipe SQLite schema version"}
	}
	if a.target.pipePipe {
		return nil
	}
	p := string(in.Probe)
	if !strings.Contains(p, `"subscriptions"`) || !strings.Contains(p, `"app_version`) {
		return nil
	}
	return &Detection{NewPipe, nil, 98, "NewPipe subscription fields"}
}

func (a newPipeAdapter) Decode(in *Input, sink Sink) error {
	if in.Archive == nil {
		return decodeNewPipeJSON(in, sink)
	}
	if a.Detect(in) == nil {
		return invalid("Unsupported NewPipe backup")
	}
	return withArchiveDatabase(in, func(db *sql.DB) error { return readNewPipeDatabase(db, sink) })
}

func decodeNewPipeJSON(in *Input, sink Sink) error {
	f, err := os.Open(in.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	var doc struct {
		Subscriptions *[]struct {
			ServiceID int    `json:"service_id"`
			URL       string `json:"url"`
			Name      string `json:"name"`
		} `json:"subscriptions"`
	}
	if err := json.NewDecoder(bufio.NewReader(f)).Decode(&doc); err != nil {
		return invalid("Backup root must be a JSON object")
	}
	if doc.Subscriptions == nil {
		return invalid("NewPipe backup does not contain subscriptions")
	}
	if err := sink.MarkCategory(Subscriptions); err != nil {
		return err
	}
	for _, s := range *doc.Subscriptions {
		switch {
		case s.ServiceID != 0:
			err = sink.Issue(Issue{Subscriptions, "unsupported_subscription_provider", "A non-YouTube NewPipe subscription was skipped", 1})
		case strings.TrimSpace(s.URL) != "":
			_, err = sink.Write(&Record{Type: TypeSubscription, ChannelURL: strings.TrimSpace(s.URL), Name: strings.TrimSpace(s.Name)})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// withArchiveDatabase extracts the archive's database next to the upload and opens it read-only and immutable.
func withArchiveDatabase(in *Input, fn func(*sql.DB) error) error {
	name := databaseEntry(in)
	if name == "" {
		return invalid("Backup database is missing")
	}
	data, err := in.OpenEntry(name)
	if err != nil {
		return err
	}
	if len(data) < 1 || len(data) > MaxArchiveEntryBytes {
		return invalid("Backup database is outside the allowed range")
	}
	tmp, err := os.CreateTemp(filepath.Dir(in.Path), "portability-newpipe-*.sqlite")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	tmp.Close()
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+tmp.Name()+"?mode=ro&immutable=1&_pragma=query_only(1)&_pragma=trusted_schema(0)&_pragma=cell_size_check(1)")
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(db)
}

func hasTables(db *sql.DB, names ...string) bool {
	for _, n := range names {
		var one int
		if db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND lower(name)=lower(?)", n).Scan(&one) != nil {
			return false
		}
	}
	return true
}

// rows runs a query and hands every row to fn as a column map, missing values read as zero.
func rows(db *sql.DB, q string, fn func(map[string]any) error) error {
	rs, err := db.Query(q)
	if err != nil {
		return invalid("Backup database is unreadable")
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		if err := fn(m); err != nil {
			return err
		}
	}
	return rs.Err()
}

func sqlStr(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return ""
}

func sqlInt(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func rowVideo(m map[string]any) *Video {
	return &Video{URL: sqlStr(m, "url"), Title: sqlStr(m, "title"), ThumbnailURL: sqlStr(m, "thumbnail_url"), DurationSeconds: sqlInt(m, "duration"),
		ChannelName: sqlStr(m, "uploader"), ChannelURL: sqlStr(m, "uploader_url"), PublishedAt: -1}
}

// readNewPipeDatabase is NewPipeDatabasePortabilityReader.
func readNewPipeDatabase(db *sql.DB, sink Sink) error {
	write := func(r *Record) error {
		_, err := sink.Write(r)
		return err
	}
	table := func(c Category, t, q string, fn func(map[string]any) error, required ...string) error {
		if !hasTables(db, append([]string{t}, required...)...) {
			return nil
		}
		if err := sink.MarkCategory(c); err != nil {
			return err
		}
		return rows(db, q, fn)
	}
	steps := []func() error{
		func() error {
			return table(Subscriptions, "subscriptions", "SELECT service_id, url, name, avatar_url FROM subscriptions ORDER BY uid", func(m map[string]any) error {
				if sqlInt(m, "service_id") == 0 {
					return write(&Record{Type: TypeSubscription, ChannelURL: sqlStr(m, "url"), Name: sqlStr(m, "name"), AvatarURL: sqlStr(m, "avatar_url")})
				}
				return sink.Issue(Issue{Subscriptions, "unsupported_subscription_provider", "A non-YouTube subscription was skipped", 1})
			})
		},
		func() error {
			err := table(SubscriptionGroups, "feed_group", "SELECT uid, name FROM feed_group ORDER BY sort_order, uid", func(m map[string]any) error {
				return write(&Record{Type: TypeGroup, Name: sqlStr(m, "name")})
			})
			if err != nil || !hasTables(db, "feed_group_subscription_join", "feed_group", "subscriptions") {
				return err
			}
			return rows(db, `SELECT g.name AS group_name, s.url AS channel_url FROM feed_group_subscription_join j
JOIN feed_group g ON g.uid = j.group_id JOIN subscriptions s ON s.uid = j.subscription_id ORDER BY j.group_id, j.subscription_id`, func(m map[string]any) error {
				return write(&Record{Type: TypeGroupMembership, GroupName: sqlStr(m, "group_name"), ChannelURL: sqlStr(m, "channel_url")})
			})
		},
		func() error {
			return table(History, "stream_history", `SELECT h.access_date, s.url, s.title, s.duration, s.uploader, s.uploader_url, s.thumbnail_url
FROM stream_history h JOIN streams s ON s.uid = h.stream_id ORDER BY h.access_date, h.stream_id`, func(m map[string]any) error {
				return write(&Record{Type: TypeHistory, Video: rowVideo(m), WatchedAt: sqlInt(m, "access_date")})
			}, "streams")
		},
		func() error {
			err := table(Playlists, "playlists", "SELECT uid, name FROM playlists ORDER BY display_index, uid", func(m map[string]any) error {
				return write(&Record{Type: TypePlaylist, SourceID: strconv.FormatInt(sqlInt(m, "uid"), 10), Name: sqlStr(m, "name")})
			})
			if err == nil && hasTables(db, "playlist_stream_join", "streams") {
				err = rows(db, `SELECT j.playlist_id, j.join_index, s.url, s.title, s.duration, s.uploader, s.uploader_url, s.thumbnail_url
FROM playlist_stream_join j JOIN streams s ON s.uid = j.stream_id ORDER BY j.playlist_id, j.join_index`, func(m map[string]any) error {
					return write(&Record{Type: TypePlaylistVideo, PlaylistSourceID: strconv.FormatInt(sqlInt(m, "playlist_id"), 10),
						Position: int(sqlInt(m, "join_index")), Video: rowVideo(m)})
				})
			}
			if err != nil {
				return err
			}
			return table(SavedPlaylists, "remote_playlists", "SELECT uid, url, name, thumbnail_url, uploader, stream_count FROM remote_playlists ORDER BY display_index, uid",
				func(m map[string]any) error {
					return write(&Record{Type: TypeSavedPlaylist, SourceID: strconv.FormatInt(sqlInt(m, "uid"), 10), URL: sqlStr(m, "url"), Title: sqlStr(m, "name"),
						ThumbnailURL: sqlStr(m, "thumbnail_url"), UploaderName: sqlStr(m, "uploader"), StreamCount: sqlInt(m, "stream_count")})
				})
		},
		func() error {
			return table(Progress, "stream_state", "SELECT s.url, state.progress_time FROM stream_state state JOIN streams s ON s.uid = state.stream_id",
				func(m map[string]any) error {
					return write(&Record{Type: TypeProgress, VideoURL: sqlStr(m, "url"), PositionSeconds: sqlInt(m, "progress_time")})
				}, "streams")
		},
		func() error {
			return table(SearchHistory, "search_history", "SELECT search, creation_date FROM search_history ORDER BY creation_date, id", func(m map[string]any) error {
				return write(&Record{Type: TypeSearchHistory, Term: sqlStr(m, "search"), SearchedAt: sqlInt(m, "creation_date")})
			})
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// newPipeService is NewPipeProvider.serviceId: YouTube 0, Bilibili 5, NicoNico 6.
func newPipeService(raw string) (int, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	h := strings.ToLower(u.Hostname())
	switch {
	case h == "youtube.com" || h == "youtu.be" || strings.HasSuffix(h, ".youtube.com"):
		return 0, true
	case h == "bilibili.com" || strings.HasSuffix(h, ".bilibili.com") || h == "b23.tv":
		return 5, true
	case h == "nicovideo.jp" || strings.HasSuffix(h, ".nicovideo.jp") || h == "nico.ms":
		return 6, true
	}
	return 0, false
}

func (t newPipeTarget) supported(u string) bool {
	id, ok := newPipeService(u)
	return ok && (t.pipePipe || id == 0)
}

func recordURLs(r *Record) []string {
	switch r.Type {
	case TypeSubscription, TypeGroupMembership:
		return []string{r.ChannelURL}
	case TypeHistory, TypePlaylistVideo:
		if r.Video != nil {
			return []string{r.Video.URL}
		}
	case TypeProgress:
		return []string{r.VideoURL}
	case TypeSavedPlaylist:
		return []string{r.URL}
	}
	return nil
}

// AssessExport adds the records the target app cannot hold to the unsupported categories.
func (a newPipeAdapter) AssessExport(src Source, cats []Category) []Issue {
	var issues []Issue
	for _, c := range cats {
		if !exportable(a.Descriptor(), c) {
			issues = append(issues, Issue{c, "unsupported_export_category", string(c) + " cannot be exported", 1})
		}
	}
	for _, c := range cats {
		n := int64(0)
		src.ForEach(c, func(r *Record) error {
			for _, u := range recordURLs(r) {
				if !a.target.supported(u) {
					n++
				}
			}
			return nil
		})
		if n > 0 {
			issues = append(issues, Issue{c, "unsupported_provider", a.target.name + " cannot represent one or more provider records", n})
		}
	}
	return issues
}
