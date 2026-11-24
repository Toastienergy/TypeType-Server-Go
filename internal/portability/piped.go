package portability

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
)

// pipedAdapter is PipedPortabilityAdapter: the Piped export object, or a bare array of channel ids.
type pipedAdapter struct{}

var (
	libreTubeFields = []string{`"watchPositions"`, `"customInstances"`, `"playlistBookmarks"`, `"localPlaylists"`}
	versionField    = regexp.MustCompile(`"version"\s*:\s*(\d+)`)
)

func (pipedAdapter) Descriptor() Descriptor {
	return Descriptor{Piped, 1, []Capability{both(Subscriptions, Complete), both(SubscriptionGroups, Complete), both(History, Complete),
		both(Playlists, Partial)}, "json", "application/json"}
}

func (pipedAdapter) AutoDetect() bool { return true }

func (pipedAdapter) Detect(in *Input) *Detection {
	if in.Archive != nil {
		return nil
	}
	p := string(in.Probe)
	for _, f := range libreTubeFields {
		if strings.Contains(p, f) {
			return nil
		}
	}
	if strings.Contains(p, `"format"`) && strings.Contains(p, `"Piped"`) {
		var v *string
		if m := versionField.FindStringSubmatch(p); m != nil {
			v = &m[1]
		}
		return &Detection{Piped, v, 96, "Piped format marker"}
	}
	if strings.HasPrefix(strings.TrimLeft(p, " \t\r\n"), "[") && (strings.Contains(p, "UC") || strings.Contains(p, "youtube.com/channel/")) {
		return &Detection{Piped, nil, 70, "Piped subscription array"}
	}
	return nil
}

func pipedSubscriptions(d *json.Decoder, sink Sink) error {
	if err := sink.MarkCategory(Subscriptions); err != nil {
		return err
	}
	return eachValue(d, func(v any) error {
		m, _ := v.(map[string]any)
		channel := scalarText(v)
		if m != nil {
			// Kotlin stops at channelId, which is "" when missing, and so drops Piped's own url entries.
			channel = firstNonBlank(jstr(m, "channelId"), jstr(m, "url"))
		}
		if strings.TrimSpace(channel) == "" {
			return nil
		}
		_, err := sink.Write(&Record{Type: TypeSubscription, ChannelURL: youtubeChannelURL(channel), Name: firstNonBlank(jstr(m, "name"), jstr(m, "channelName")),
			AvatarURL: firstNonBlank(jstr(m, "avatar"), jstr(m, "channelThumbnail")), SubscribedAt: jlong(m, "subscribedAt")})
		return err
	})
}

func (pipedAdapter) Decode(in *Input, sink Sink) error {
	f, err := os.Open(in.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(bufio.NewReaderSize(f, 64<<10))
	d.UseNumber()
	t, err := d.Token()
	if err != nil {
		return invalid("Invalid Piped backup")
	}
	if t == json.Delim('[') {
		return pipedSubscriptions(d, sink)
	}
	if t != json.Delim('{') {
		return invalid("Invalid Piped backup")
	}
	write := func(r *Record) error {
		_, err := sink.Write(r)
		return err
	}
	format, version := "", -1
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return invalid("Invalid Piped backup")
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
			if err := openArray(d, "Invalid Piped subscriptions"); err != nil {
				return err
			}
			err = pipedSubscriptions(d, sink)
		case "groups", "channelGroups":
			if err = openArray(d, "Invalid Piped groups"); err == nil {
				err = sink.MarkCategory(SubscriptionGroups)
			}
			if err == nil {
				err = eachValue(d, func(v any) error {
					m, _ := v.(map[string]any)
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
			}
		case "watchHistory":
			if err = openArray(d, "Invalid Piped history"); err == nil {
				err = sink.MarkCategory(History)
			}
			if err == nil {
				err = eachValue(d, func(v any) error {
					m, _ := v.(map[string]any)
					id := jstr(m, "videoId")
					if strings.TrimSpace(id) == "" {
						return nil
					}
					return write(&Record{Type: TypeHistory, Video: &Video{URL: youtubeVideoURL(id), Title: jstr(m, "title"), ThumbnailURL: jstr(m, "thumbnail"),
						DurationSeconds: jlong(m, "duration"), ChannelName: jstr(m, "uploaderName"), ChannelURL: jstr(m, "uploaderUrl"), PublishedAt: -1},
						WatchedAt: jlong(m, "watchedAt"), PositionSeconds: jlong(m, "currentTime")})
				})
			}
		case "playlists":
			if err = openArray(d, "Invalid Piped playlists"); err == nil {
				err = sink.MarkCategory(Playlists)
			}
			i := 0
			if err == nil {
				err = eachValue(d, func(v any) error {
					m, _ := v.(map[string]any)
					name := jstr(m, "name")
					id := "piped:" + itoa(i) + ":" + strings.ToLower(name)
					i++
					if err := write(&Record{Type: TypePlaylist, SourceID: id, Name: name}); err != nil {
						return err
					}
					for pos, x := range jarr(m, "videos") {
						if err := write(&Record{Type: TypePlaylistVideo, PlaylistSourceID: id, Position: pos, Video: &Video{URL: youtubeVideoURL(scalarText(x)), PublishedAt: -1}}); err != nil {
							return err
						}
					}
					return nil
				})
			}
		default:
			err = skip(d)
		}
		if err != nil {
			return err
		}
	}
	if format != "Piped" || version != 1 {
		return invalid("Unsupported Piped backup version")
	}
	return nil
}

func (pipedAdapter) Encode(src Source, w io.Writer, cats []Category) error {
	j := &jsonWriter{w: w}
	j.raw(`{"format":"Piped","version":1`)
	list := func(name string, c Category, each func(*Record, *int)) error {
		if !hasCategory(cats, c) {
			return nil
		}
		j.raw(`,"` + name + `":[`)
		n := 0
		var recs []*Record
		if err := src.ForEach(c, func(r *Record) error { recs = append(recs, r); return nil }); err != nil {
			return err
		}
		for _, r := range recs {
			each(r, &n)
		}
		j.raw("]")
		return j.err
	}
	sep := func(n *int) {
		if *n > 0 {
			j.raw(",")
		}
		*n++
	}
	err := list("subscriptions", Subscriptions, func(r *Record, n *int) {
		sep(n)
		j.str(youtubeID(r.ChannelURL))
	})
	if err == nil {
		err = list("groups", SubscriptionGroups, func(r *Record, n *int) {
			if r.Type != TypeGroup {
				return
			}
			sep(n)
			j.raw(`{"groupName":`)
			j.str(r.Name)
			j.raw(`,"channels":[`)
			m := 0
			src.ForEachChild(SubscriptionGroups, r.Name, func(c *Record) error {
				sep(&m)
				j.str(youtubeID(c.ChannelURL))
				return nil
			})
			j.raw("]}")
		})
	}
	if err == nil {
		err = list("watchHistory", History, func(r *Record, n *int) {
			if r.Video == nil {
				return
			}
			sep(n)
			v := r.Video
			j.fields("videoId", youtubeID(v.URL), "title", v.Title, "uploaderName", v.ChannelName, "uploaderUrl", v.ChannelURL,
				"thumbnail", v.ThumbnailURL, "duration", v.DurationSeconds, "watchedAt", r.WatchedAt, "currentTime", r.PositionSeconds)
		})
	}
	if err == nil {
		err = list("playlists", Playlists, func(r *Record, n *int) {
			if r.Type != TypePlaylist {
				return
			}
			sep(n)
			j.raw(`{"name":`)
			j.str(r.Name)
			j.raw(`,"type":"playlist","visibility":"private","videos":[`)
			m := 0
			src.ForEachChild(Playlists, r.SourceID, func(c *Record) error {
				if c.Video != nil {
					sep(&m)
					j.str(c.Video.URL)
				}
				return nil
			})
			j.raw("]}")
		})
	}
	if err != nil {
		return err
	}
	j.raw("}")
	return j.err
}
