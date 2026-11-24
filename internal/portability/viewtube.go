package portability

import (
	"archive/zip"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// viewTubeAdapter is ViewTubePortabilityAdapter: user.json inside a ViewTube export archive, import only.
type viewTubeAdapter struct{}

func (viewTubeAdapter) Descriptor() Descriptor {
	return Descriptor{ViewTube, 1, []Capability{importOnly(Subscriptions, Complete), importOnly(History, Complete), importOnly(Progress, Complete)},
		"zip", "application/zip"}
}

func (viewTubeAdapter) AutoDetect() bool { return true }

func (viewTubeAdapter) Detect(in *Input) *Detection {
	if in.Archive == nil || !in.Archive.Has("user.json") {
		return nil
	}
	z, err := zip.OpenReader(in.Path)
	if err != nil {
		return nil
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != "user.json" {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return nil
		}
		b, _ := io.ReadAll(io.LimitReader(r, ProbeBytes))
		r.Close()
		p := string(b)
		for _, k := range []string{`"username"`, `"subscriptions"`, `"history"`, `"settings"`} {
			if !strings.Contains(p, k) {
				return nil
			}
		}
		return &Detection{ViewTube, nil, 99, "ViewTube user.json export"}
	}
	return nil
}

func firstURL(v any) string {
	for _, x := range asList(v) {
		if m, ok := x.(map[string]any); ok {
			if u := jstr(m, "url"); strings.TrimSpace(u) != "" {
				return u
			}
		}
	}
	return ""
}

func asList(v any) []any {
	a, _ := v.([]any)
	return a
}

func (a viewTubeAdapter) Decode(in *Input, sink Sink) error {
	if a.Detect(in) == nil {
		return invalid("Unsupported ViewTube backup")
	}
	data, err := in.OpenEntry("user.json")
	if err != nil {
		return err
	}
	var doc map[string]any
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.UseNumber()
	if err := d.Decode(&doc); err != nil {
		return invalid("Backup root must be a JSON object")
	}
	write := func(r *Record) error {
		_, err := sink.Write(r)
		return err
	}
	if subs, ok := doc["subscriptions"].(map[string]any); ok {
		channels := asList(subs["channels"])
		if len(channels) > MaxContainerRecords {
			return invalid("channels contains too many records")
		}
		for _, c := range channels {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			avatar := jstr(m, "authorThumbnailUrl")
			if strings.TrimSpace(avatar) == "" {
				avatar = firstURL(m["authorThumbnails"])
			}
			ch := jstr(m, "authorUrl")
			if strings.TrimSpace(ch) == "" {
				ch = youtubeChannelURL(jstr(m, "authorId"))
			}
			if strings.TrimSpace(ch) != "" {
				if err := write(&Record{Type: TypeSubscription, ChannelURL: ch, Name: jstr(m, "author"), AvatarURL: avatar}); err != nil {
					return err
				}
			}
		}
	}
	if hist, ok := doc["history"].(map[string]any); ok {
		videos := asList(hist["videos"])
		if len(videos) > MaxContainerRecords {
			return invalid("videos contains too many records")
		}
		for _, x := range videos {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			det, _ := m["videoDetails"].(map[string]any)
			id := firstNonBlank(jstr(det, "videoId"), jstr(m, "videoId"))
			u := youtubeVideoURL(id)
			if u == "" {
				continue
			}
			duration := jlong(m, "lengthSeconds")
			if _, ok := det["lengthSeconds"]; ok {
				duration = jlong(det, "lengthSeconds")
			}
			published := int64(-1)
			if _, ok := det["published"]; ok {
				published = jlong(det, "published")
			}
			// ViewTube dates visits as ISO instants; Kotlin keeps their seconds where TypeType expects milliseconds.
			var watched int64
			if t, err := time.Parse(time.RFC3339Nano, jstr(m, "lastVisit")); err == nil {
				watched = t.UnixMilli()
			}
			progress := jlong(m, "progressSeconds")
			v := &Video{URL: u, Title: jstr(det, "title"), ThumbnailURL: firstURL(det["videoThumbnails"]), DurationSeconds: duration,
				ChannelName: jstr(det, "author"), ChannelURL: youtubeChannelURL(jstr(det, "authorId")), ChannelAvatarURL: jstr(det, "authorThumbnailUrl"),
				ViewCount: jlong(det, "viewCount"), PublishedAt: published}
			if err := write(&Record{Type: TypeHistory, Video: v, WatchedAt: watched, PositionSeconds: progress}); err != nil {
				return err
			}
			if err := write(&Record{Type: TypeProgress, VideoURL: u, PositionSeconds: progress * 1000, UpdatedAt: watched}); err != nil {
				return err
			}
		}
	}
	if _, ok := doc["settings"]; ok {
		return sink.Issue(Issue{Settings, "viewtube_settings_ignored", "ViewTube settings are not portable to TypeType", 1})
	}
	return nil
}

func (viewTubeAdapter) Encode(Source, io.Writer, []Category) error {
	return invalid("ViewTube does not provide a compatible full-backup import")
}
