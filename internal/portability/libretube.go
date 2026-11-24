package portability

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// libreTubeAdapter is LibreTubePortabilityAdapter: a Piped-format export with LibreTube's own sections.
type libreTubeAdapter struct{}

var libreTubeUnique = []string{`"watchPositions"`, `"customInstances"`, `"playlistBookmarks"`, `"localPlaylists"`, `"preferences"`}

func (libreTubeAdapter) Descriptor() Descriptor {
	return Descriptor{LibreTube, 1, []Capability{both(Subscriptions, Complete), both(SubscriptionGroups, Complete), both(History, Partial),
		both(Progress, Complete), both(SearchHistory, Partial), both(Playlists, Partial), both(SavedPlaylists, Complete)}, "json", "application/json"}
}

func (libreTubeAdapter) AutoDetect() bool { return true }

func (libreTubeAdapter) Detect(in *Input) *Detection {
	if in.Archive != nil {
		return nil
	}
	p := string(in.Probe)
	if !strings.Contains(p, `"format"`) || !strings.Contains(p, `"Piped"`) {
		return nil
	}
	for _, f := range libreTubeUnique {
		if strings.Contains(p, f) {
			var v *string
			if m := versionField.FindStringSubmatch(p); m != nil {
				v = &m[1]
			}
			return &Detection{LibreTube, v, 99, "LibreTube field " + f}
		}
	}
	return nil
}

func libreVideo(m map[string]any) *Video {
	return &Video{URL: youtubeVideoURL(jstr(m, "videoId")), Title: jstr(m, "title"), ThumbnailURL: jstr(m, "thumbnailUrl"), DurationSeconds: jlong(m, "duration"),
		ChannelName: jstr(m, "uploader"), ChannelURL: jstr(m, "uploaderUrl"), ChannelAvatarURL: jstr(m, "uploaderAvatar"), PublishedAt: -1}
}

func (libreTubeAdapter) Decode(in *Input, sink Sink) error {
	f, err := os.Open(in.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(bufio.NewReaderSize(f, 64<<10))
	d.UseNumber()
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return invalid("Backup root must be a JSON object")
	}
	write := func(r *Record) error {
		_, err := sink.Write(r)
		return err
	}
	section := func(c Category, fn func(map[string]any) error) error {
		if err := openArray(d, "Invalid LibreTube backup section"); err != nil {
			return err
		}
		if err := sink.MarkCategory(c); err != nil {
			return err
		}
		return eachValue(d, func(v any) error {
			m, _ := v.(map[string]any)
			return fn(m)
		})
	}
	format, version := "", -1
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return invalid("Invalid LibreTube backup")
		}
		field, _ := t.(string)
		switch field {
		case "format":
			var v any
			d.Decode(&v)
			format = scalarText(v)
		case "version":
			var v json.Number
			if d.Decode(&v) == nil {
				if n, err := v.Int64(); err == nil {
					version = int(n)
				}
			}
		case "subscriptions", "localSubscriptions":
			if err = openArray(d, "Invalid Piped subscriptions"); err == nil {
				err = pipedSubscriptions(d, sink)
			}
		case "groups", "channelGroups":
			err = section(SubscriptionGroups, func(m map[string]any) error {
				name := firstNonBlank(jstr(m, "groupName"), jstr(m, "name"))
				if strings.TrimSpace(name) == "" {
					return nil
				}
				if err := write(&Record{Type: TypeGroup, Name: name}); err != nil {
					return err
				}
				for _, c := range jarr(m, "channels") {
					if s := scalarText(c); strings.TrimSpace(s) != "" {
						if err := write(&Record{Type: TypeGroupMembership, GroupName: name, ChannelURL: youtubeChannelURL(s)}); err != nil {
							return err
						}
					}
				}
				return nil
			})
		case "watchHistory":
			err = section(History, func(m map[string]any) error {
				if strings.TrimSpace(jstr(m, "videoId")) == "" {
					return nil
				}
				return write(&Record{Type: TypeHistory, Video: libreVideo(m)})
			})
		case "watchPositions":
			err = section(Progress, func(m map[string]any) error {
				if strings.TrimSpace(jstr(m, "videoId")) == "" {
					return nil
				}
				// LibreTube and TypeType both keep the position in milliseconds; Kotlin divides by 1000.
				return write(&Record{Type: TypeProgress, VideoURL: youtubeVideoURL(jstr(m, "videoId")), PositionSeconds: jlong(m, "position")})
			})
		case "searchHistory":
			err = section(SearchHistory, func(m map[string]any) error {
				if q := jstr(m, "query"); strings.TrimSpace(q) != "" {
					return write(&Record{Type: TypeSearchHistory, Term: q})
				}
				return nil
			})
		case "localPlaylists":
			err = section(Playlists, func(m map[string]any) error {
				p, ok := m["playlist"].(map[string]any)
				if !ok {
					return nil
				}
				id := "libretube:" + jstr(p, "id") + ":" + strings.ToLower(jstr(p, "name"))
				if err := write(&Record{Type: TypePlaylist, SourceID: id, Name: jstr(p, "name"), Description: jstr(p, "description")}); err != nil {
					return err
				}
				for pos, v := range jarr(m, "videos") {
					vm, _ := v.(map[string]any)
					if err := write(&Record{Type: TypePlaylistVideo, PlaylistSourceID: id, Position: pos, Video: libreVideo(vm)}); err != nil {
						return err
					}
				}
				return nil
			})
		case "playlistBookmarks":
			err = section(SavedPlaylists, func(m map[string]any) error {
				id := jstr(m, "playlistId")
				if strings.TrimSpace(id) == "" {
					return nil
				}
				return write(&Record{Type: TypeSavedPlaylist, SourceID: id, URL: "https://www.youtube.com/playlist?list=" + id, Title: jstr(m, "playlistName"),
					ThumbnailURL: jstr(m, "thumbnailUrl"), UploaderName: jstr(m, "uploader"), StreamCount: jlong(m, "videos")})
			})
		default:
			err = skip(d)
		}
		if err != nil {
			return err
		}
	}
	if format != "Piped" || version != 1 {
		return invalid("Unsupported LibreTube backup version")
	}
	return nil
}

func (libreTubeAdapter) Encode(src Source, w io.Writer, cats []Category) error {
	j := &jsonWriter{w: w}
	j.raw(`{"format":"Piped","version":1`)
	var err error
	section := func(name string, c Category, each func(*Record) bool) {
		if err != nil || !hasCategory(cats, c) {
			return
		}
		j.raw(`,"` + name + `":[`)
		var recs []*Record
		err = src.ForEach(c, func(r *Record) error { recs = append(recs, r); return nil })
		n := 0
		for _, r := range recs {
			var b strings.Builder
			out := j.w
			j.w = &b
			ok := each(r)
			j.w = out
			if ok {
				if n > 0 {
					j.raw(",")
				}
				j.raw(b.String())
				n++
			}
		}
		j.raw("]")
	}
	section("localSubscriptions", Subscriptions, func(r *Record) bool {
		j.fields("channelId", youtubeID(r.ChannelURL), "name", r.Name, "avatar", r.AvatarURL)
		return true
	})
	section("channelGroups", SubscriptionGroups, func(r *Record) bool {
		if r.Type != TypeGroup {
			return false
		}
		j.raw(`{"groupName":`)
		j.str(r.Name)
		j.raw(`,"channels":[`)
		n := 0
		src.ForEachChild(SubscriptionGroups, r.Name, func(c *Record) error {
			if n > 0 {
				j.raw(",")
			}
			j.str(youtubeID(c.ChannelURL))
			n++
			return nil
		})
		j.raw(`],"index":0}`)
		return true
	})
	section("watchHistory", History, func(r *Record) bool {
		if r.Video == nil {
			return false
		}
		v := r.Video
		j.fields("videoId", youtubeID(v.URL), "title", v.Title, "uploader", v.ChannelName, "uploaderUrl", v.ChannelURL, "uploaderAvatar", v.ChannelAvatarURL,
			"thumbnailUrl", v.ThumbnailURL, "duration", v.DurationSeconds)
		return true
	})
	section("watchPositions", Progress, func(r *Record) bool {
		j.fields("videoId", youtubeID(r.VideoURL), "position", r.PositionSeconds)
		return true
	})
	section("searchHistory", SearchHistory, func(r *Record) bool {
		j.fields("query", r.Term)
		return true
	})
	section("localPlaylists", Playlists, func(r *Record) bool {
		if r.Type != TypePlaylist {
			return false
		}
		j.raw(`{"playlist":{"id":0,"name":`)
		j.str(r.Name)
		j.raw(`,"description":`)
		j.str(r.Description)
		j.raw(`,"thumbnailUrl":""},"videos":[`)
		n := 0
		src.ForEachChild(Playlists, r.SourceID, func(c *Record) error {
			if c.Video == nil {
				return nil
			}
			if n > 0 {
				j.raw(",")
			}
			v := c.Video
			j.fields("id", 0, "playlistId", 0, "videoId", youtubeID(v.URL), "title", v.Title, "thumbnailUrl", v.ThumbnailURL, "duration", v.DurationSeconds,
				"uploader", v.ChannelName, "uploaderUrl", v.ChannelURL, "uploaderAvatar", v.ChannelAvatarURL)
			n++
			return nil
		})
		j.raw("]}")
		return true
	})
	section("playlistBookmarks", SavedPlaylists, func(r *Record) bool {
		j.fields("playlistId", r.SourceID, "playlistName", r.Title, "thumbnailUrl", r.ThumbnailURL, "uploader", r.UploaderName, "videos", r.StreamCount)
		return true
	})
	if err != nil {
		return err
	}
	j.raw("}")
	return j.err
}
