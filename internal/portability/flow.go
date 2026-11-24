package portability

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// flowAdapter is FlowPortabilityAdapter: the Flow app's version 2 backup.
type flowAdapter struct{}

func (flowAdapter) Descriptor() Descriptor {
	return Descriptor{Flow, 2, []Capability{both(Subscriptions, Complete), both(SubscriptionGroups, Complete), both(History, Complete),
		both(Progress, Complete), both(SearchHistory, Complete), both(Favorites, Complete), both(ContentFilters, Partial),
		importOnly(Playlists, Partial)}, "json", "application/json"}
}

func (flowAdapter) AutoDetect() bool { return true }

func (flowAdapter) Detect(in *Input) *Detection {
	if in.Archive != nil {
		return nil
	}
	p := string(in.Probe)
	if !strings.Contains(p, `"viewHistory"`) || !strings.Contains(p, `"subscriptionGroups"`) {
		return nil
	}
	if !strings.Contains(p, `"playlistVideos"`) && !strings.Contains(p, `"likedVideos"`) {
		return nil
	}
	var v *string
	if m := versionField.FindStringSubmatch(p); m != nil {
		v = &m[1]
	}
	return &Detection{Flow, v, 98, "Flow backup fields"}
}

func (flowAdapter) Decode(in *Input, sink Sink) error {
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
		t, err := d.Token()
		if err != nil {
			return invalid("Invalid Flow backup section")
		}
		if t == nil {
			return nil
		}
		if t != json.Delim('[') {
			return invalid("Invalid Flow backup section")
		}
		if err := sink.MarkCategory(c); err != nil {
			return err
		}
		return eachValue(d, func(v any) error {
			m, _ := v.(map[string]any)
			return fn(m)
		})
	}
	version := -1
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return invalid("Invalid Flow backup")
		}
		field, _ := t.(string)
		switch field {
		case "version":
			var v json.Number
			if d.Decode(&v) == nil {
				if n, err := v.Int64(); err == nil {
					version = int(n)
				}
			}
		case "subscriptions":
			err = section(Subscriptions, func(m map[string]any) error {
				id := jstr(m, "channelId")
				if strings.TrimSpace(id) == "" {
					return nil
				}
				return write(&Record{Type: TypeSubscription, ChannelURL: youtubeChannelURL(id), Name: jstr(m, "channelName"),
					AvatarURL: jstr(m, "channelThumbnail"), SubscribedAt: jlong(m, "subscribedAt")})
			})
		case "subscriptionGroups":
			err = section(SubscriptionGroups, func(m map[string]any) error {
				name := jstr(m, "name")
				if strings.TrimSpace(name) == "" {
					return nil
				}
				if err := write(&Record{Type: TypeGroup, Name: name}); err != nil {
					return err
				}
				n := 0
				for _, ch := range strings.Split(jstr(m, "channelIds"), ",") {
					if ch = strings.TrimSpace(ch); ch == "" {
						continue
					}
					if n >= MaxContainerRecords {
						return invalid("Flow group contains too many channels")
					}
					n++
					if err := write(&Record{Type: TypeGroupMembership, GroupName: name, ChannelURL: youtubeChannelURL(ch)}); err != nil {
						return err
					}
				}
				return nil
			})
		case "viewHistory":
			if err = sink.MarkCategory(Progress); err == nil {
				err = section(History, func(m map[string]any) error {
					id := jstr(m, "videoId")
					if strings.TrimSpace(id) == "" {
						return nil
					}
					v := &Video{URL: youtubeVideoURL(id), Title: jstr(m, "title"), ThumbnailURL: jstr(m, "thumbnailUrl"), DurationSeconds: jlong(m, "duration") / 1000,
						ChannelName: jstr(m, "channelName"), ChannelURL: youtubeChannelURL(jstr(m, "channelId")), PublishedAt: -1}
					pos, at := jlong(m, "position"), jlong(m, "timestamp")
					if err := write(&Record{Type: TypeHistory, Video: v, WatchedAt: at, PositionSeconds: pos / 1000}); err != nil {
						return err
					}
					// Flow positions are milliseconds, as TypeType progress is; Kotlin divides them here too.
					return write(&Record{Type: TypeProgress, VideoURL: v.URL, PositionSeconds: pos, UpdatedAt: at})
				})
			}
		case "searchHistory":
			err = section(SearchHistory, func(m map[string]any) error {
				if q := jstr(m, "query"); strings.TrimSpace(q) != "" {
					return write(&Record{Type: TypeSearchHistory, Term: q, SearchedAt: jlong(m, "timestamp")})
				}
				return nil
			})
		case "playlists":
			err = section(Playlists, func(m map[string]any) error {
				if id := jstr(m, "id"); strings.TrimSpace(id) != "" {
					return write(&Record{Type: TypePlaylist, SourceID: id, Name: jstr(m, "name"), Description: jstr(m, "description"), CreatedAt: jlong(m, "createdAt")})
				}
				return nil
			})
		case "playlistVideos":
			err = section(Playlists, func(m map[string]any) error {
				pl, vid := jstr(m, "playlistId"), jstr(m, "videoId")
				if strings.TrimSpace(pl) == "" || strings.TrimSpace(vid) == "" {
					return nil
				}
				pos := min(max(jlong(m, "position"), 0), 1<<31-1)
				return write(&Record{Type: TypePlaylistVideo, PlaylistSourceID: pl, Position: int(pos), Video: &Video{URL: youtubeVideoURL(vid), PublishedAt: -1},
					AddedAt: jlong(m, "addedAt")})
			})
		case "likedVideos":
			err = section(Favorites, func(m map[string]any) error {
				id := jstr(m, "videoId")
				if strings.TrimSpace(id) == "" {
					return nil
				}
				return write(&Record{Type: TypeFavorite, Video: &Video{URL: youtubeVideoURL(id), Title: jstr(m, "title"), ThumbnailURL: jstr(m, "thumbnail"),
					ChannelName: jstr(m, "channelName"), PublishedAt: -1}, FavoritedAt: jlong(m, "likedAt")})
			})
		case "contentPreferences":
			var m map[string]any
			if d.Decode(&m) != nil || m == nil {
				return invalid("Invalid Flow content preferences")
			}
			if err = sink.MarkCategory(ContentFilters); err == nil {
				for _, c := range jarr(m, "blockedChannels") {
					if s := scalarText(c); strings.TrimSpace(s) != "" {
						if err = write(&Record{Type: TypeContentFilter, Kind: "blockedChannel", Value: youtubeChannelURL(s)}); err != nil {
							break
						}
					}
				}
			}
		default:
			err = skip(d)
		}
		if err != nil {
			return err
		}
	}
	if version != 2 {
		return invalid("Unsupported Flow backup version")
	}
	return nil
}

func (flowAdapter) Encode(src Source, w io.Writer, cats []Category) error {
	j := &jsonWriter{w: w}
	j.raw(`{"version":2,"timestamp":` + strconv.FormatInt(time.Now().UnixMilli(), 10))
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
	section("subscriptions", Subscriptions, func(r *Record) bool {
		j.fields("channelId", youtubeID(r.ChannelURL), "channelName", r.Name, "channelThumbnail", r.AvatarURL, "subscribedAt", r.SubscribedAt)
		return true
	})
	section("subscriptionGroups", SubscriptionGroups, func(r *Record) bool {
		if r.Type != TypeGroup {
			return false
		}
		var ids []string
		size := 0
		src.ForEachChild(SubscriptionGroups, r.Name, func(c *Record) error {
			id := youtubeID(c.ChannelURL)
			size += len(id) + 1
			if len(ids) >= MaxContainerRecords || size > MaxRecordJSONBytes {
				err = invalid("Flow group channel list is too large")
				return err
			}
			ids = append(ids, id)
			return nil
		})
		j.fields("name", r.Name, "channelIds", strings.Join(ids, ","), "sortOrder", 0)
		return true
	})
	section("viewHistory", History, func(r *Record) bool {
		if r.Video == nil {
			return false
		}
		v := r.Video
		j.fields("videoId", youtubeID(v.URL), "position", r.PositionSeconds*1000, "duration", v.DurationSeconds*1000, "timestamp", r.WatchedAt,
			"title", v.Title, "thumbnailUrl", v.ThumbnailURL, "channelName", v.ChannelName, "channelId", youtubeID(v.ChannelURL))
		return true
	})
	section("searchHistory", SearchHistory, func(r *Record) bool {
		j.fields("query", r.Term, "timestamp", r.SearchedAt, "type", "TEXT")
		return true
	})
	section("likedVideos", Favorites, func(r *Record) bool {
		if r.Video == nil {
			return false
		}
		var b strings.Builder
		out := j.w
		j.w = &b
		j.fields("videoId", youtubeID(r.Video.URL), "title", r.Video.Title, "thumbnail", r.Video.ThumbnailURL, "channelName", r.Video.ChannelName,
			"likedAt", r.FavoritedAt)
		j.w = out
		j.raw(strings.TrimSuffix(b.String(), "}") + `,"isMusic":false}`)
		return true
	})
	if err == nil && hasCategory(cats, ContentFilters) {
		j.raw(`,"contentPreferences":{"blockedChannels":[`)
		n := 0
		err = src.ForEach(ContentFilters, func(r *Record) error {
			if r.Kind == "blockedChannel" {
				if n > 0 {
					j.raw(",")
				}
				j.str(youtubeID(r.Value))
				n++
			}
			return nil
		})
		j.raw(`],"preferredTopics":[],"blockedTopics":[]}`)
	}
	if err != nil {
		return err
	}
	j.raw("}")
	return j.err
}
