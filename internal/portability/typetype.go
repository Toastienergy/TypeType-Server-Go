package portability

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// typeTypeAdapter is TypeTypePortabilityAdapter: the "typetype-backup" version 1 file, every category complete.
type typeTypeAdapter struct {
	// settings turns a settings section into the stored settings JSON, with defaults and without unknown keys.
	settings func([]byte) ([]byte, error)
}

var (
	typeTypeMarker  = regexp.MustCompile(`"format"\s*:\s*"typetype-backup"`)
	typeTypeVersion = regexp.MustCompile(`"version"\s*:\s*(\d+)`)
)

func (typeTypeAdapter) Descriptor() Descriptor {
	caps := make([]Capability, len(Categories))
	for i, c := range Categories {
		caps[i] = both(c, Complete)
	}
	return Descriptor{TypeType, 1, caps, "json", "application/json"}
}

func (typeTypeAdapter) AutoDetect() bool { return true }

func (typeTypeAdapter) Detect(in *Input) *Detection {
	if in.Archive != nil || !typeTypeMarker.Match(in.Probe) {
		return nil
	}
	var v *string
	if m := typeTypeVersion.FindSubmatch(in.Probe); m != nil {
		s := string(m[1])
		v = &s
	}
	return &Detection{TypeType, v, 100, "TypeType backup marker"}
}

type ttVideo struct {
	URL           string `json:"url"`
	VideoURL      string `json:"videoUrl"`
	Title         string `json:"title"`
	Thumbnail     string `json:"thumbnail"`
	Duration      int64  `json:"duration"`
	ChannelName   string `json:"channelName"`
	ChannelURL    string `json:"channelUrl"`
	ChannelAvatar string `json:"channelAvatar"`
	ViewCount     int64  `json:"viewCount"`
	PublishedAt   *int64 `json:"publishedAt"`
	Progress      int64  `json:"progress"`
	WatchedAt     int64  `json:"watchedAt"`
	Position      int    `json:"position"`
	AddedAt       int64  `json:"addedAt"`
	FavoritedAt   int64  `json:"favoritedAt"`
}

func (v ttVideo) video(url string) *Video {
	pub := int64(-1)
	if v.PublishedAt != nil {
		pub = *v.PublishedAt
	}
	return &Video{URL: url, Title: v.Title, ThumbnailURL: v.Thumbnail, DurationSeconds: v.Duration, ChannelName: v.ChannelName,
		ChannelURL: v.ChannelURL, ChannelAvatarURL: v.ChannelAvatar, ViewCount: v.ViewCount, PublishedAt: pub}
}

type ttFilterItem struct {
	URL          string  `json:"url"`
	Keyword      string  `json:"keyword"`
	Name         *string `json:"name"`
	Title        *string `json:"title"`
	ThumbnailURL *string `json:"thumbnailUrl"`
	UploaderName *string `json:"uploaderName"`
	BlockedAt    int64   `json:"blockedAt"`
	AllowedAt    int64   `json:"allowedAt"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// eachElement decodes every element of the array the decoder is positioned on; null is an empty section.
func eachElement[T any](d *json.Decoder, fn func(T) error) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if t == nil {
		return nil
	}
	if t != json.Delim('[') {
		return invalid("Invalid TypeType backup section")
	}
	for d.More() {
		var x T
		if err := d.Decode(&x); err != nil {
			return invalid("Invalid TypeType backup section")
		}
		if err := fn(x); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func (a typeTypeAdapter) Decode(in *Input, sink Sink) error {
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
	var format string
	version := -1
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return invalid("Invalid TypeType backup")
		}
		field, _ := t.(string)
		mark := func(c Category) error { return sink.MarkCategory(c) }
		switch field {
		case "format":
			var v any
			d.Decode(&v)
			format, _ = v.(string)
		case "version":
			var v json.Number
			if d.Decode(&v) == nil {
				if n, err := v.Int64(); err == nil {
					version = int(n)
				}
			}
		case "subscriptions":
			err = mark(Subscriptions)
			if err == nil {
				err = eachElement(d, func(s struct {
					ChannelURL   string `json:"channelUrl"`
					Name         string `json:"name"`
					AvatarURL    string `json:"avatarUrl"`
					SubscribedAt int64  `json:"subscribedAt"`
				}) error {
					return write(&Record{Type: TypeSubscription, ChannelURL: s.ChannelURL, Name: s.Name, AvatarURL: s.AvatarURL, SubscribedAt: s.SubscribedAt})
				})
			}
		case "subscriptionGroups":
			err = mark(SubscriptionGroups)
			if err == nil {
				err = eachElement(d, func(g struct {
					Name        string   `json:"name"`
					ChannelURLs []string `json:"channelUrls"`
				}) error {
					if err := write(&Record{Type: TypeGroup, Name: g.Name}); err != nil {
						return err
					}
					for _, u := range g.ChannelURLs {
						if err := write(&Record{Type: TypeGroupMembership, GroupName: g.Name, ChannelURL: u}); err != nil {
							return err
						}
					}
					return nil
				})
			}
		case "history":
			err = mark(History)
			if err == nil {
				err = eachElement(d, func(h ttVideo) error {
					v := h.video(h.URL)
					v.ViewCount, v.PublishedAt = 0, -1
					return write(&Record{Type: TypeHistory, Video: v, WatchedAt: h.WatchedAt, PositionSeconds: h.Progress})
				})
			}
		case "playlists":
			err = mark(Playlists)
			if err == nil {
				err = eachElement(d, func(p struct {
					ID          string    `json:"id"`
					Name        string    `json:"name"`
					Description string    `json:"description"`
					CreatedAt   int64     `json:"createdAt"`
					Videos      []ttVideo `json:"videos"`
				}) error {
					id := p.ID
					if id == "" {
						id = p.Name + ":" + strconv.FormatInt(p.CreatedAt, 10)
					}
					if err := write(&Record{Type: TypePlaylist, SourceID: id, Name: p.Name, Description: p.Description, CreatedAt: p.CreatedAt}); err != nil {
						return err
					}
					for _, v := range p.Videos {
						if err := write(&Record{Type: TypePlaylistVideo, PlaylistSourceID: id, Position: v.Position, Video: v.video(v.URL), AddedAt: v.AddedAt}); err != nil {
							return err
						}
					}
					return nil
				})
			}
		case "watchLater":
			err = mark(WatchLater)
			if err == nil {
				err = eachElement(d, func(v ttVideo) error {
					return write(&Record{Type: TypeWatchLater, Video: v.video(v.URL), AddedAt: v.AddedAt})
				})
			}
		case "favorites":
			err = mark(Favorites)
			if err == nil {
				err = eachElement(d, func(v ttVideo) error {
					return write(&Record{Type: TypeFavorite, Video: v.video(v.VideoURL), FavoritedAt: v.FavoritedAt})
				})
			}
		case "progress":
			err = mark(Progress)
			if err == nil {
				err = eachElement(d, func(p struct {
					VideoURL  string `json:"videoUrl"`
					Position  int64  `json:"position"`
					UpdatedAt int64  `json:"updatedAt"`
				}) error {
					return write(&Record{Type: TypeProgress, VideoURL: p.VideoURL, PositionSeconds: p.Position, UpdatedAt: p.UpdatedAt})
				})
			}
		case "searchHistory":
			err = mark(SearchHistory)
			if err == nil {
				err = eachElement(d, func(s struct {
					Term       string `json:"term"`
					SearchedAt int64  `json:"searchedAt"`
				}) error {
					return write(&Record{Type: TypeSearchHistory, Term: s.Term, SearchedAt: s.SearchedAt})
				})
			}
		case "savedPlaylists":
			err = mark(SavedPlaylists)
			if err == nil {
				err = eachElement(d, func(s struct {
					PublicPlaylistID string `json:"publicPlaylistId"`
					URL              string `json:"url"`
					Title            string `json:"title"`
					ThumbnailURL     string `json:"thumbnailUrl"`
					UploaderName     string `json:"uploaderName"`
					StreamCount      int64  `json:"streamCount"`
					PlaylistType     string `json:"playlistType"`
					SavedAt          int64  `json:"savedAt"`
				}) error {
					return write(&Record{Type: TypeSavedPlaylist, SourceID: s.PublicPlaylistID, URL: s.URL, Title: s.Title, ThumbnailURL: s.ThumbnailURL,
						UploaderName: s.UploaderName, StreamCount: s.StreamCount, PlaylistType: s.PlaylistType, SavedAt: s.SavedAt})
				})
			}
		case "settings", "recommender":
			err = mark(Settings)
			var raw json.RawMessage
			if err == nil && d.Decode(&raw) != nil {
				err = invalid("Invalid TypeType backup section")
			}
			if err == nil && string(raw) != "null" {
				if field == "settings" && a.settings != nil {
					raw, err = a.settings(raw)
				}
				typ := TypeSettings
				if field == "recommender" {
					typ = TypeRecommender
				}
				if err == nil {
					err = write(&Record{Type: typ, Values: raw})
				}
			}
		case "contentFilters":
			err = mark(ContentFilters)
			var cf map[string][]ttFilterItem
			if err == nil && d.Decode(&cf) != nil {
				err = invalid("Invalid TypeType backup section")
			}
			for _, k := range []string{"blockedChannels", "blockedVideos", "blockedKeywords", "allowedChannels", "allowedPlaylists"} {
				for _, it := range cf[k] {
					if err != nil {
						break
					}
					r := &Record{Type: TypeContentFilter, Value: it.URL, Label: str(it.Name), ImageURL: str(it.ThumbnailURL), CreatedAt: it.BlockedAt}
					switch k {
					case "blockedChannels":
						r.Kind = "blockedChannel"
					case "blockedVideos":
						r.Kind = "blockedVideo"
					case "blockedKeywords":
						r.Kind, r.Value, r.Label, r.ImageURL = "blockedKeyword", it.Keyword, "", ""
					case "allowedChannels":
						r.Kind, r.CreatedAt = "allowedChannel", it.AllowedAt
					case "allowedPlaylists":
						r.Kind, r.Label, r.CreatedAt = "allowedPlaylist", str(it.Title), it.AllowedAt
						r.Metadata, _ = json.Marshal(map[string]string{"uploaderName": str(it.UploaderName)})
					}
					err = write(r)
				}
			}
		default:
			var skip json.RawMessage
			err = d.Decode(&skip)
		}
		if err != nil {
			return err
		}
	}
	if format != "typetype-backup" {
		return invalid("Unsupported TypeType backup format")
	}
	if version != 1 {
		return invalid("Unsupported TypeType backup version")
	}
	return nil
}

// jsonWriter writes JSON values as Jackson does: no HTML escaping.
type jsonWriter struct {
	w   io.Writer
	err error
}

func (j *jsonWriter) raw(s string) {
	if j.err == nil {
		_, j.err = io.WriteString(j.w, s)
	}
}

func (j *jsonWriter) str(s string) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(s)
	j.raw(string(bytes.TrimRight(b.Bytes(), "\n")))
}

func (j *jsonWriter) field(name string, first *bool) {
	if !*first {
		j.raw(",")
	}
	*first = false
	j.str(name)
	j.raw(":")
}

func (j *jsonWriter) fields(pairs ...any) {
	first := true
	j.raw("{")
	for i := 0; i+1 < len(pairs); i += 2 {
		j.field(pairs[i].(string), &first)
		switch v := pairs[i+1].(type) {
		case string:
			j.str(v)
		case int64:
			j.raw(strconv.FormatInt(v, 10))
		case int:
			j.raw(strconv.Itoa(v))
		}
	}
	j.raw("}")
}

func videoFields(v *Video) []any {
	return []any{"title", v.Title, "thumbnail", v.ThumbnailURL, "duration", v.DurationSeconds, "channelName", v.ChannelName,
		"channelUrl", v.ChannelURL, "channelAvatar", v.ChannelAvatarURL, "viewCount", v.ViewCount, "publishedAt", v.PublishedAt}
}

func (typeTypeAdapter) Encode(src Source, w io.Writer, cats []Category) error {
	j := &jsonWriter{w: w}
	want := map[Category]bool{}
	for _, c := range cats {
		want[c] = true
	}
	legacy := map[string]bool{}
	for _, c := range cats {
		if c == SubscriptionGroups {
			legacy[string(Subscriptions)] = true
		} else {
			legacy[string(c)] = true
		}
	}
	var names []string
	for n := range legacy {
		names = append(names, n)
	}
	sort.Strings(names)
	first := true
	j.raw("{")
	j.field("format", &first)
	j.str("typetype-backup")
	j.field("version", &first)
	j.raw("1")
	j.field("exportedAt", &first)
	j.raw(strconv.FormatInt(time.Now().UnixMilli(), 10))
	j.field("categories", &first)
	j.raw("[")
	for i, n := range names {
		if i > 0 {
			j.raw(",")
		}
		j.str(n)
	}
	j.raw("]")
	// array writes one section; fn writes a record and reports whether it did, so skipped records leave no comma.
	array := func(first *bool, name string, c Category, fn func(*Record) bool) error {
		j.field(name, first)
		j.raw("[")
		n := 0
		err := src.ForEach(c, func(r *Record) error {
			var b bytes.Buffer
			out := j.w
			j.w = &b
			ok := fn(r)
			j.w = out
			if ok {
				if n > 0 {
					j.raw(",")
				}
				j.raw(b.String())
				n++
			}
			return j.err
		})
		j.raw("]")
		if err != nil {
			return err
		}
		return j.err
	}
	var err error
	if want[Subscriptions] || want[SubscriptionGroups] {
		err = array(&first, "subscriptions", Subscriptions, func(r *Record) bool {
			if r.Type != TypeSubscription {
				return false
			}
			j.fields("channelUrl", r.ChannelURL, "name", r.Name, "avatarUrl", r.AvatarURL, "subscribedAt", r.SubscribedAt)
			return true
		})
	}
	if err == nil && want[SubscriptionGroups] {
		err = array(&first, "subscriptionGroups", SubscriptionGroups, func(r *Record) bool {
			if r.Type != TypeGroup {
				return false
			}
			var urls []string
			src.ForEachChild(SubscriptionGroups, r.Name, func(c *Record) error {
				if c.Type == TypeGroupMembership {
					urls = append(urls, c.ChannelURL)
				}
				return nil
			})
			f := true
			j.raw("{")
			j.field("name", &f)
			j.str(r.Name)
			j.field("channelUrls", &f)
			j.raw("[")
			for i, u := range urls {
				if i > 0 {
					j.raw(",")
				}
				j.str(u)
			}
			j.raw("],\"createdAt\":0,\"updatedAt\":0}")
			return true
		})
	}
	if err == nil && want[History] {
		err = array(&first, "history", History, func(r *Record) bool {
			if r.Type != TypeHistory || r.Video == nil {
				return false
			}
			j.fields(append(append([]any{"url", r.Video.URL}, videoFields(r.Video)...), "progress", r.PositionSeconds, "watchedAt", r.WatchedAt)...)
			return true
		})
	}
	if err == nil && want[Playlists] {
		err = array(&first, "playlists", Playlists, func(r *Record) bool {
			if r.Type != TypePlaylist {
				return false
			}
			var vids []*Record
			src.ForEachChild(Playlists, r.SourceID, func(c *Record) error {
				if c.Type == TypePlaylistVideo && c.Video != nil {
					vids = append(vids, c)
				}
				return nil
			})
			f := true
			j.raw("{")
			for _, kv := range [][2]string{{"id", r.SourceID}, {"name", r.Name}, {"description", r.Description}} {
				j.field(kv[0], &f)
				j.str(kv[1])
			}
			j.field("videos", &f)
			j.raw("[")
			for i, v := range vids {
				if i > 0 {
					j.raw(",")
				}
				j.fields(append(append([]any{"url", v.Video.URL}, videoFields(v.Video)...), "position", v.Position, "addedAt", v.AddedAt)...)
			}
			j.raw(fmt.Sprintf("],\"videoCount\":%d,\"createdAt\":%d}", len(vids), r.CreatedAt))
			return true
		})
	}
	if err == nil && want[WatchLater] {
		err = array(&first, "watchLater", WatchLater, func(r *Record) bool {
			if r.Type != TypeWatchLater || r.Video == nil {
				return false
			}
			j.fields(append([]any{"url", r.Video.URL, "addedAt", r.AddedAt}, videoFields(r.Video)...)...)
			return true
		})
	}
	if err == nil && want[Favorites] {
		err = array(&first, "favorites", Favorites, func(r *Record) bool {
			if r.Type != TypeFavorite || r.Video == nil {
				return false
			}
			j.fields(append([]any{"videoUrl", r.Video.URL, "favoritedAt", r.FavoritedAt}, videoFields(r.Video)...)...)
			return true
		})
	}
	if err == nil && want[Progress] {
		err = array(&first, "progress", Progress, func(r *Record) bool {
			if r.Type != TypeProgress {
				return false
			}
			j.fields("videoUrl", r.VideoURL, "position", r.PositionSeconds, "updatedAt", r.UpdatedAt)
			return true
		})
	}
	if err == nil && want[SearchHistory] {
		err = array(&first, "searchHistory", SearchHistory, func(r *Record) bool {
			if r.Type != TypeSearchHistory {
				return false
			}
			j.fields("term", r.Term, "searchedAt", r.SearchedAt)
			return true
		})
	}
	if err == nil && want[SavedPlaylists] {
		err = array(&first, "savedPlaylists", SavedPlaylists, func(r *Record) bool {
			if r.Type != TypeSavedPlaylist {
				return false
			}
			j.fields("id", r.SourceID, "publicPlaylistId", r.SourceID, "url", r.URL, "title", r.Title, "thumbnailUrl", r.ThumbnailURL,
				"uploaderName", r.UploaderName, "streamCount", r.StreamCount, "playlistType", r.PlaylistType, "savedAt", r.SavedAt)
			return true
		})
	}
	if err == nil && want[Settings] {
		var settings, recommender []json.RawMessage
		err = src.ForEach(Settings, func(r *Record) error {
			switch r.Type {
			case TypeSettings:
				settings = append(settings, r.Values)
			case TypeRecommender:
				recommender = append(recommender, r.Values)
			}
			return nil
		})
		j.field("settings", &first)
		if len(settings) > 0 {
			j.raw(string(settings[0]))
		} else {
			j.raw("null")
		}
		for _, r := range recommender {
			j.field("recommender", &first)
			j.raw(string(r))
		}
	}
	if err == nil && want[ContentFilters] {
		j.field("contentFilters", &first)
		j.raw("{")
		inner := true
		for _, kv := range [][2]string{{"blockedChannels", "blockedChannel"}, {"blockedVideos", "blockedVideo"}, {"blockedKeywords", "blockedKeyword"},
			{"allowedChannels", "allowedChannel"}, {"allowedPlaylists", "allowedPlaylist"}} {
			kind := kv[1]
			err = array(&inner, kv[0], ContentFilters, func(r *Record) bool {
				if r.Type != TypeContentFilter || r.Kind != kind {
					return false
				}
				key := "url"
				if kind == "blockedKeyword" {
					key = "keyword"
				}
				switch kind {
				case "allowedPlaylist":
					var m struct {
						UploaderName string `json:"uploaderName"`
					}
					json.Unmarshal(r.Metadata, &m)
					j.fields(key, r.Value, "title", r.Label, "thumbnailUrl", r.ImageURL, "uploaderName", m.UploaderName, "allowedAt", r.CreatedAt)
				case "allowedChannel":
					j.fields(key, r.Value, "name", r.Label, "thumbnailUrl", r.ImageURL, "allowedAt", r.CreatedAt)
				default:
					j.fields(key, r.Value, "name", r.Label, "thumbnailUrl", r.ImageURL, "blockedAt", r.CreatedAt)
				}
				return true
			})
			if err != nil {
				break
			}
		}
		j.raw("}")
	}
	j.raw("}")
	if err != nil {
		return err
	}
	return j.err
}
