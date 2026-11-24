package portability

import (
	"archive/zip"
	"database/sql"
	"io"
	"os"
	"strconv"
)

func (t newPipeTarget) schema() []string {
	streams := "CREATE TABLE streams (uid INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, service_id INTEGER NOT NULL, url TEXT NOT NULL, title TEXT NOT NULL, stream_type TEXT NOT NULL, duration INTEGER NOT NULL, uploader TEXT NOT NULL, uploader_url TEXT, thumbnail_url TEXT, view_count INTEGER, textual_upload_date TEXT, upload_date INTEGER, is_upload_date_approximation INTEGER"
	playlists := "CREATE TABLE playlists (uid INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, name TEXT, is_thumbnail_permanent INTEGER NOT NULL, thumbnail_stream_id INTEGER NOT NULL, display_index INTEGER NOT NULL)"
	if t.pipePipe {
		streams += ", is_paid INTEGER NOT NULL"
		playlists = "CREATE TABLE playlists (uid INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, name TEXT, thumbnail_url TEXT, display_index INTEGER NOT NULL)"
	}
	out := []string{
		"CREATE TABLE subscriptions (uid INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, service_id INTEGER NOT NULL, url TEXT, name TEXT, avatar_url TEXT, subscriber_count INTEGER, description TEXT, notification_mode INTEGER NOT NULL)",
		"CREATE UNIQUE INDEX index_subscriptions_service_id_url ON subscriptions(service_id,url)",
		"CREATE TABLE search_history (creation_date INTEGER, service_id INTEGER NOT NULL, search TEXT, id INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL)",
		"CREATE INDEX index_search_history_search ON search_history(search)",
		streams + ")",
		"CREATE UNIQUE INDEX index_streams_service_id_url ON streams(service_id,url)",
		"CREATE TABLE stream_history (stream_id INTEGER NOT NULL, access_date INTEGER NOT NULL, repeat_count INTEGER NOT NULL, PRIMARY KEY(stream_id,access_date), FOREIGN KEY(stream_id) REFERENCES streams(uid) ON UPDATE CASCADE ON DELETE CASCADE)",
		"CREATE INDEX index_stream_history_stream_id ON stream_history(stream_id)",
		"CREATE TABLE stream_state (stream_id INTEGER NOT NULL PRIMARY KEY, progress_time INTEGER NOT NULL, FOREIGN KEY(stream_id) REFERENCES streams(uid) ON UPDATE CASCADE ON DELETE CASCADE)",
		playlists,
		"CREATE TABLE playlist_stream_join (playlist_id INTEGER NOT NULL, stream_id INTEGER NOT NULL, join_index INTEGER NOT NULL, PRIMARY KEY(playlist_id,join_index), FOREIGN KEY(playlist_id) REFERENCES playlists(uid) ON UPDATE CASCADE ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED, FOREIGN KEY(stream_id) REFERENCES streams(uid) ON UPDATE CASCADE ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED)",
		"CREATE UNIQUE INDEX index_playlist_stream_join_playlist_id_join_index ON playlist_stream_join(playlist_id,join_index)",
		"CREATE INDEX index_playlist_stream_join_stream_id ON playlist_stream_join(stream_id)",
		"CREATE TABLE remote_playlists (uid INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, service_id INTEGER NOT NULL, name TEXT, url TEXT, thumbnail_url TEXT, uploader TEXT, display_index INTEGER NOT NULL, stream_count INTEGER)",
		"CREATE UNIQUE INDEX index_remote_playlists_service_id_url ON remote_playlists(service_id,url)",
		"CREATE TABLE feed (stream_id INTEGER NOT NULL, subscription_id INTEGER NOT NULL, PRIMARY KEY(stream_id,subscription_id), FOREIGN KEY(stream_id) REFERENCES streams(uid) ON UPDATE CASCADE ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED, FOREIGN KEY(subscription_id) REFERENCES subscriptions(uid) ON UPDATE CASCADE ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED)",
		"CREATE INDEX index_feed_subscription_id ON feed(subscription_id)",
		"CREATE TABLE feed_group (uid INTEGER PRIMARY KEY AUTOINCREMENT NOT NULL, name TEXT NOT NULL, icon_id INTEGER NOT NULL, sort_order INTEGER NOT NULL)",
		"CREATE INDEX index_feed_group_sort_order ON feed_group(sort_order)",
		"CREATE TABLE feed_group_subscription_join (group_id INTEGER NOT NULL, subscription_id INTEGER NOT NULL, PRIMARY KEY(group_id,subscription_id), FOREIGN KEY(group_id) REFERENCES feed_group(uid) ON UPDATE CASCADE ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED, FOREIGN KEY(subscription_id) REFERENCES subscriptions(uid) ON UPDATE CASCADE ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED)",
		"CREATE INDEX index_feed_group_subscription_join_subscription_id ON feed_group_subscription_join(subscription_id)",
		"CREATE TABLE feed_last_updated (subscription_id INTEGER NOT NULL PRIMARY KEY, last_updated INTEGER, FOREIGN KEY(subscription_id) REFERENCES subscriptions(uid) ON UPDATE CASCADE ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED)",
	}
	if t.pipePipe {
		out = append(out, "CREATE INDEX index_playlists_name ON playlists(name)", "CREATE INDEX index_remote_playlists_name ON remote_playlists(name)")
	}
	return append(out, "PRAGMA user_version = "+strconv.Itoa(t.version),
		"CREATE TABLE IF NOT EXISTS room_master_table (id INTEGER PRIMARY KEY,identity_hash TEXT)",
		"INSERT OR REPLACE INTO room_master_table (id,identity_hash) VALUES(42,'"+t.identity+"')")
}

// archiveWriter is NewPipeArchiveRecordWriter over one transaction.
type archiveWriter struct {
	tx *sql.Tx
	t  newPipeTarget
}

func (w archiveWriter) id(q string, args ...any) (int64, error) {
	var id int64
	err := w.tx.QueryRow(q, args...).Scan(&id)
	return id, err
}

func (w archiveWriter) insertID(q string, args ...any) (int64, error) {
	r, err := w.tx.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (w archiveWriter) subscription(u, name, avatar string) (int64, error) {
	svc, _ := newPipeService(u)
	if _, err := w.tx.Exec("INSERT OR IGNORE INTO subscriptions(service_id,url,name,avatar_url,subscriber_count,description,notification_mode) VALUES(?,?,?,?,NULL,NULL,0)",
		svc, u, name, avatar); err != nil {
		return 0, err
	}
	return w.id("SELECT uid FROM subscriptions WHERE service_id=? AND url=?", svc, u)
}

func (w archiveWriter) video(v *Video) (int64, error) {
	svc, _ := newPipeService(v.URL)
	var published any
	if v.PublishedAt >= 0 {
		published = v.PublishedAt
	}
	cols, vals := "service_id,url,title,stream_type,duration,uploader,uploader_url,thumbnail_url,view_count,textual_upload_date,upload_date,is_upload_date_approximation", "?,?,?,?,?,?,?,?,?,NULL,?,0"
	if w.t.pipePipe {
		cols, vals = cols+",is_paid", vals+",0"
	}
	if _, err := w.tx.Exec("INSERT OR IGNORE INTO streams("+cols+") VALUES("+vals+")", svc, v.URL, v.Title, "VIDEO_STREAM", v.DurationSeconds,
		v.ChannelName, v.ChannelURL, v.ThumbnailURL, v.ViewCount, published); err != nil {
		return 0, err
	}
	return w.id("SELECT uid FROM streams WHERE service_id=? AND url=?", svc, v.URL)
}

func (w archiveWriter) write(src Source, cats []Category) error {
	t := w.t
	steps := []struct {
		c  Category
		fn func() error
	}{
		{Subscriptions, func() error {
			return src.ForEach(Subscriptions, func(r *Record) error {
				if !t.supported(r.ChannelURL) {
					return nil
				}
				_, err := w.subscription(r.ChannelURL, r.Name, r.AvatarURL)
				return err
			})
		}},
		{SubscriptionGroups, func() error {
			order := 0
			var groups []*Record
			src.ForEach(SubscriptionGroups, func(r *Record) error {
				if r.Type == TypeGroup {
					groups = append(groups, r)
				}
				return nil
			})
			for _, g := range groups {
				gid, err := w.insertID("INSERT INTO feed_group(name,icon_id,sort_order) VALUES(?,?,?)", g.Name, 0, order)
				if err != nil {
					return err
				}
				order++
				err = src.ForEachChild(SubscriptionGroups, g.Name, func(m *Record) error {
					if m.Type != TypeGroupMembership || !t.supported(m.ChannelURL) {
						return nil
					}
					sid, err := w.subscription(m.ChannelURL, "", "")
					if err == nil {
						_, err = w.tx.Exec("INSERT OR IGNORE INTO feed_group_subscription_join(group_id,subscription_id) VALUES(?,?)", gid, sid)
					}
					return err
				})
				if err != nil {
					return err
				}
			}
			return nil
		}},
		{History, func() error {
			return src.ForEach(History, func(r *Record) error {
				if r.Video == nil || !t.supported(r.Video.URL) {
					return nil
				}
				sid, err := w.video(r.Video)
				if err == nil {
					_, err = w.tx.Exec("INSERT OR IGNORE INTO stream_history(stream_id,access_date,repeat_count) VALUES(?,?,1)", sid, r.WatchedAt)
				}
				return err
			})
		}},
		{Playlists, func() error {
			order := 0
			var lists []*Record
			src.ForEach(Playlists, func(r *Record) error {
				if r.Type == TypePlaylist {
					lists = append(lists, r)
				}
				return nil
			})
			for _, p := range lists {
				var pid int64
				var err error
				if t.pipePipe {
					pid, err = w.insertID("INSERT INTO playlists(name,thumbnail_url,display_index) VALUES(?,'',?)", p.Name, order)
				} else {
					pid, err = w.insertID("INSERT INTO playlists(name,is_thumbnail_permanent,thumbnail_stream_id,display_index) VALUES(?,0,-1,?)", p.Name, order)
				}
				if err != nil {
					return err
				}
				order++
				err = src.ForEachChild(Playlists, p.SourceID, func(v *Record) error {
					if v.Type != TypePlaylistVideo || v.Video == nil || !t.supported(v.Video.URL) {
						return nil
					}
					sid, err := w.video(v.Video)
					if err == nil {
						_, err = w.tx.Exec("INSERT OR REPLACE INTO playlist_stream_join(playlist_id,stream_id,join_index) VALUES(?,?,?)", pid, sid, v.Position)
					}
					return err
				})
				if err != nil {
					return err
				}
			}
			return nil
		}},
		{Progress, func() error {
			return src.ForEach(Progress, func(r *Record) error {
				if !t.supported(r.VideoURL) {
					return nil
				}
				sid, err := w.video(&Video{URL: r.VideoURL, PublishedAt: -1})
				if err == nil {
					_, err = w.tx.Exec("INSERT OR REPLACE INTO stream_state(stream_id,progress_time) VALUES(?,?)", sid, r.PositionSeconds)
				}
				return err
			})
		}},
		{SearchHistory, func() error {
			return src.ForEach(SearchHistory, func(r *Record) error {
				_, err := w.tx.Exec("INSERT INTO search_history(creation_date,service_id,search) VALUES(?,0,?)", r.SearchedAt, r.Term)
				return err
			})
		}},
		{SavedPlaylists, func() error {
			order := 0
			return src.ForEach(SavedPlaylists, func(r *Record) error {
				svc, ok := newPipeService(r.URL)
				if !ok || !t.pipePipe && svc != 0 {
					return nil
				}
				_, err := w.tx.Exec("INSERT OR IGNORE INTO remote_playlists(service_id,name,url,thumbnail_url,uploader,display_index,stream_count) VALUES(?,?,?,?,?,?,?)",
					svc, r.Title, r.URL, r.ThumbnailURL, r.UploaderName, order, r.StreamCount)
				order++
				return err
			})
		}},
	}
	for _, s := range steps {
		if hasCategory(cats, s.c) {
			if err := s.fn(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Encode is NewPipeArchivePortabilityWriter: a fresh database in the target's schema, zipped as newpipe.db.
func (a newPipeAdapter) Encode(src Source, out io.Writer, cats []Category) error {
	f, err := os.CreateTemp("", "typetype-portability-*.db")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	defer os.Remove(name + "-journal")
	db, err := sql.Open("sqlite", "file:"+name+"?_pragma=foreign_keys(1)&_pragma=journal_mode(DELETE)")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	for _, q := range a.target.schema() {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return err
		}
	}
	tx, err := db.Begin()
	if err == nil {
		if err = (archiveWriter{tx, a.target}).write(src, cats); err != nil {
			tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}
	db.Close()
	if err != nil {
		return err
	}
	z := zip.NewWriter(out)
	e, err := z.Create("newpipe.db")
	if err != nil {
		return err
	}
	data, err := os.Open(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(e, data)
	data.Close()
	if err != nil {
		return err
	}
	return z.Close()
}
