package portability

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// grayjayAdapter is GrayjayPortabilityAdapter: an unencrypted Grayjay export, exportInfo plus one store per kind.
// Grayjay dates history in epoch seconds and keeps positions in seconds (HistoryVideo.toReconString); TypeType
// dates in milliseconds and keeps progress in milliseconds, which Kotlin does not convert.
type grayjayAdapter struct{}

var grayjayVersion = regexp.MustCompile(`"version"\s*:\s*"([^"]+)"`)

func (grayjayAdapter) Descriptor() Descriptor {
	return Descriptor{Grayjay, 1, []Capability{both(Subscriptions, Partial), both(SubscriptionGroups, Complete), both(History, Partial),
		importOnly(Progress, Complete), both(Playlists, Partial), both(WatchLater, Partial)}, "zip", "application/zip"}
}

func (grayjayAdapter) AutoDetect() bool { return true }

func (grayjayAdapter) Detect(in *Input) *Detection {
	if in.Archive == nil || !in.Archive.Has("exportInfo") {
		return nil
	}
	stores := false
	for _, e := range in.Archive.Entries {
		stores = stores || strings.HasPrefix(e.Name, "stores/")
	}
	if !stores {
		return nil
	}
	data, err := in.OpenEntry("exportInfo")
	if err != nil {
		return nil
	}
	if len(data) > ProbeBytes {
		data = data[:ProbeBytes]
	}
	m := grayjayVersion.FindSubmatch(data)
	if m == nil || string(m[1]) != "1" {
		return nil
	}
	v := "1"
	return &Detection{Grayjay, &v, 99, "Grayjay exportInfo and stores"}
}

func grayjayStore(in *Input, name string, fn func(string) error) error {
	if !in.Archive.Has(name) {
		return nil
	}
	data, err := in.OpenEntry(name)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if t, err := d.Token(); err != nil || t != json.Delim('[') {
		return invalid(name + " must contain an array")
	}
	count := 0
	for d.More() {
		count++
		if count > MaxContainerRecords {
			return invalid(name + " contains too many records")
		}
		var s string
		if err := d.Decode(&s); err != nil {
			return invalid(name + " contains an invalid record")
		}
		if err := fn(s); err != nil {
			return err
		}
	}
	return nil
}

func (a grayjayAdapter) Decode(in *Input, sink Sink) error {
	if a.Detect(in) == nil {
		return invalid("Unsupported or encrypted Grayjay backup")
	}
	write := func(r *Record) error {
		_, err := sink.Write(r)
		return err
	}
	steps := []struct {
		name string
		fn   func(string) error
	}{
		{"stores/subscriptions", func(v string) error {
			if u := strings.TrimSpace(v); u != "" {
				return write(&Record{Type: TypeSubscription, ChannelURL: u})
			}
			return nil
		}},
		{"stores/subscription_groups", func(v string) error {
			var g struct {
				Name any   `json:"name"`
				URLs []any `json:"urls"`
			}
			if json.Unmarshal([]byte(v), &g) != nil {
				return invalid("Backup root must be a JSON object")
			}
			name := scalarText(g.Name)
			if strings.TrimSpace(name) == "" {
				return nil
			}
			if len(g.URLs) > MaxContainerRecords {
				return invalid("Grayjay group contains too many channels")
			}
			if err := write(&Record{Type: TypeGroup, Name: name}); err != nil {
				return err
			}
			for _, u := range g.URLs {
				if s, ok := u.(string); ok && strings.TrimSpace(s) != "" {
					if err := write(&Record{Type: TypeGroupMembership, GroupName: name, ChannelURL: s}); err != nil {
						return err
					}
				}
			}
			return nil
		}},
		{"stores/history", func(v string) error {
			parts := strings.SplitN(v, "|||", 4)
			if len(parts) != 4 {
				return sink.Issue(Issue{History, "grayjay_history_invalid", "A Grayjay history record could not be parsed", 1})
			}
			u := strings.TrimSpace(parts[0])
			secs, _ := strconv.ParseInt(parts[1], 10, 64)
			pos, _ := strconv.ParseInt(parts[2], 10, 64)
			if u == "" {
				return nil
			}
			if err := write(&Record{Type: TypeHistory, Video: &Video{URL: u, Title: parts[3], PublishedAt: -1}, WatchedAt: secs * 1000, PositionSeconds: pos}); err != nil {
				return err
			}
			return write(&Record{Type: TypeProgress, VideoURL: u, PositionSeconds: pos * 1000, UpdatedAt: secs * 1000})
		}},
		{"stores/playlists", func(v string) error {
			var lines []string
			for _, l := range strings.Split(v, "\n") {
				if strings.TrimSpace(l) != "" {
					lines = append(lines, l)
				}
			}
			if len(lines) == 0 {
				return nil
			}
			name, id := lines[0], lines[0]
			if i := strings.Index(lines[0], ":::"); i >= 0 {
				name, id = lines[0][:i], lines[0][i+3:]
				if strings.TrimSpace(id) == "" {
					id = name
				}
			}
			if err := write(&Record{Type: TypePlaylist, SourceID: id, Name: name}); err != nil {
				return err
			}
			for i, u := range lines[1:] {
				if err := write(&Record{Type: TypePlaylistVideo, PlaylistSourceID: id, Position: i, Video: &Video{URL: strings.TrimSpace(u), PublishedAt: -1}}); err != nil {
					return err
				}
			}
			return nil
		}},
		{"stores/watch_later", func(v string) error {
			if u := strings.TrimSpace(v); u != "" {
				return write(&Record{Type: TypeWatchLater, Video: &Video{URL: u, PublishedAt: -1}})
			}
			return nil
		}},
	}
	for _, s := range steps {
		if err := grayjayStore(in, s.name, s.fn); err != nil {
			return err
		}
	}
	return nil
}

// AssessExport adds a partial-metadata note for every category Grayjay rebuilds from URLs.
func (a grayjayAdapter) AssessExport(src Source, cats []Category) []Issue {
	var issues []Issue
	for _, c := range cats {
		if !exportable(a.Descriptor(), c) {
			issues = append(issues, Issue{c, "unsupported_export_category", string(c) + " cannot be exported", 1})
		}
	}
	for _, c := range cats {
		for _, cap := range a.Descriptor().Capabilities {
			if cap.Category == c && cap.Fidelity == Partial {
				issues = append(issues, Issue{c, "grayjay_partial_metadata", "Grayjay reconstructs this category from URLs, so some TypeType metadata is not represented", 1})
			}
		}
	}
	return issues
}

var reconText = strings.NewReplacer("|||", " ", ":::", " ", "\n", " ")

func nameUUID(v string) string {
	b := md5.Sum([]byte(v))
	b[6] = b[6]&0x0f | 0x30
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (grayjayAdapter) Encode(src Source, w io.Writer, cats []Category) error {
	z := zip.NewWriter(w)
	entry := func(name string, body func(j *jsonWriter) error) error {
		f, err := z.Create(name)
		if err != nil {
			return err
		}
		j := &jsonWriter{w: f}
		if err := body(j); err != nil {
			return err
		}
		return j.err
	}
	store := func(c Category, name string, value func(*Record) (string, bool)) error {
		if !hasCategory(cats, c) {
			return nil
		}
		var recs []*Record
		if err := src.ForEach(c, func(r *Record) error { recs = append(recs, r); return nil }); err != nil {
			return err
		}
		return entry("stores/"+name, func(j *jsonWriter) error {
			j.raw("[")
			n := 0
			for _, r := range recs {
				v, ok := value(r)
				if !ok {
					continue
				}
				if n > 0 {
					j.raw(",")
				}
				j.str(v)
				n++
			}
			j.raw("]")
			return nil
		})
	}
	err := entry("exportInfo", func(j *jsonWriter) error { j.raw(`{"version":"1"}`); return nil })
	if err == nil {
		err = store(Subscriptions, "subscriptions", func(r *Record) (string, bool) { return r.ChannelURL, true })
	}
	if err == nil {
		err = store(SubscriptionGroups, "subscription_groups", func(r *Record) (string, bool) {
			if r.Type != TypeGroup {
				return "", false
			}
			var b bytes.Buffer
			g := &jsonWriter{w: &b}
			g.raw(`{"id":`)
			g.str(nameUUID(r.StableKey()))
			g.raw(`,"name":`)
			g.str(r.Name)
			g.raw(`,"urls":[`)
			n := 0
			src.ForEachChild(SubscriptionGroups, r.Name, func(c *Record) error {
				if c.Type == TypeGroupMembership {
					if n > 0 {
						g.raw(",")
					}
					g.str(c.ChannelURL)
					n++
				}
				return nil
			})
			g.raw(`],"priority":99}`)
			if b.Len() > MaxRecordJSONBytes {
				err = invalid("Grayjay group is too large")
			}
			return b.String(), true
		})
	}
	if err == nil {
		err = store(History, "history", func(r *Record) (string, bool) {
			if r.Video == nil {
				return "", false
			}
			return r.Video.URL + "|||" + strconv.FormatInt(r.WatchedAt/1000, 10) + "|||" + strconv.FormatInt(r.PositionSeconds, 10) + "|||" + reconText.Replace(r.Video.Title), true
		})
	}
	if err == nil {
		err = store(Playlists, "playlists", func(r *Record) (string, bool) {
			if r.Type != TypePlaylist {
				return "", false
			}
			var b strings.Builder
			b.WriteString(reconText.Replace(r.Name) + ":::" + reconText.Replace(r.SourceID))
			src.ForEachChild(Playlists, r.SourceID, func(c *Record) error {
				if c.Type == TypePlaylistVideo && c.Video != nil {
					b.WriteString("\n" + strings.ReplaceAll(c.Video.URL, "\n", ""))
				}
				return nil
			})
			return b.String(), true
		})
	}
	if err == nil {
		err = store(WatchLater, "watch_later", func(r *Record) (string, bool) {
			if r.Video == nil {
				return "", false
			}
			return r.Video.URL, true
		})
	}
	if err == nil {
		err = entry("plugins", func(j *jsonWriter) error { j.raw("{}"); return nil })
	}
	if err == nil {
		err = entry("plugin_settings", func(j *jsonWriter) error { j.raw("{}"); return nil })
	}
	if err != nil {
		return err
	}
	return z.Close()
}
