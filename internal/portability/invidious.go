package portability

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// invidiousAdapter is InvidiousPortabilityAdapter: the account export with subscriptions, watch history and
// playlists as bare ids, and preferences kept aside.
type invidiousAdapter struct{}

func (invidiousAdapter) Descriptor() Descriptor {
	return Descriptor{Invidious, 1, []Capability{both(Subscriptions, Complete), both(History, Partial), both(Playlists, Partial),
		both(Settings, Partial)}, "json", "application/json"}
}

func (invidiousAdapter) AutoDetect() bool { return true }

func (invidiousAdapter) Detect(in *Input) *Detection {
	if in.Archive != nil {
		return nil
	}
	p := string(in.Probe)
	history, prefs := strings.Contains(p, `"watch_history"`), strings.Contains(p, `"preferences"`)
	if !strings.Contains(p, `"subscriptions"`) || !history && !prefs {
		return nil
	}
	return &Detection{Invidious, nil, 96, "Invidious account export fields"}
}

// objectFields walks the top-level object of a JSON file, handing each field's decoder position to fn.
func objectFields(in *Input, fn func(field string, d *json.Decoder) error) error {
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
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return invalid("Invalid backup")
		}
		name, _ := t.(string)
		if err := fn(name, d); err != nil {
			return err
		}
	}
	return nil
}

func skip(d *json.Decoder) error {
	var x json.RawMessage
	return d.Decode(&x)
}

// peekArray reports whether the next value is an array and leaves the decoder inside it; anything else is
// skipped.
func peekArray(d *json.Decoder) (bool, error) {
	var raw json.RawMessage
	if !d.More() {
		return false, nil
	}
	t, err := d.Token()
	if err != nil {
		return false, err
	}
	if t == json.Delim('[') {
		return true, nil
	}
	if t == json.Delim('{') {
		for d.More() {
			if _, err := d.Token(); err != nil {
				return false, err
			}
			if err := d.Decode(&raw); err != nil {
				return false, err
			}
		}
		_, err = d.Token()
	}
	return false, err
}

func scalarText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64, bool:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return ""
}

func (invidiousAdapter) Decode(in *Input, sink Sink) error {
	write := func(r *Record) error {
		_, err := sink.Write(r)
		return err
	}
	return objectFields(in, func(field string, d *json.Decoder) error {
		switch field {
		case "subscriptions", "watch_history", "playlists":
			ok, err := peekArray(d)
			if err != nil || !ok {
				return err
			}
			switch field {
			case "subscriptions":
				if err := sink.MarkCategory(Subscriptions); err != nil {
					return err
				}
				for d.More() {
					var v any
					if err := d.Decode(&v); err != nil {
						return invalid("Invalid Invidious subscription")
					}
					if ch := youtubeChannelURL(scalarText(v)); ch != "" {
						if err := write(&Record{Type: TypeSubscription, ChannelURL: ch}); err != nil {
							return err
						}
					}
				}
			case "watch_history":
				if err := sink.MarkCategory(History); err != nil {
					return err
				}
				missing := int64(0)
				for d.More() {
					var v any
					if err := d.Decode(&v); err != nil {
						return invalid("Invalid Invidious history")
					}
					if u := youtubeVideoURL(scalarText(v)); u != "" {
						if err := write(&Record{Type: TypeHistory, Video: &Video{URL: u, PublishedAt: -1}}); err != nil {
							return err
						}
						missing++
					}
				}
				if missing > 0 {
					if err := sink.Issue(Issue{History, "missing_history_dates", "Invidious does not include original watch dates", missing}); err != nil {
						return err
					}
				}
			case "playlists":
				if err := sink.MarkCategory(Playlists); err != nil {
					return err
				}
				for i := 0; d.More(); i++ {
					var p struct {
						Title       any   `json:"title"`
						Description any   `json:"description"`
						Videos      []any `json:"videos"`
					}
					if err := d.Decode(&p); err != nil {
						return invalid("Invalid Invidious playlist")
					}
					if len(p.Videos) > MaxContainerRecords {
						return invalid("Playlist contains too many videos")
					}
					title, desc := scalarText(p.Title), scalarText(p.Description)
					id := "invidious:" + itoa(i) + ":" + lower(title)
					if err := write(&Record{Type: TypePlaylist, SourceID: id, Name: title, Description: desc}); err != nil {
						return err
					}
					for pos, v := range p.Videos {
						if err := write(&Record{Type: TypePlaylistVideo, PlaylistSourceID: id, Position: pos, Video: &Video{URL: youtubeVideoURL(scalarText(v)), PublishedAt: -1}}); err != nil {
							return err
						}
					}
				}
			}
			_, err = d.Token()
			return err
		case "preferences":
			var raw json.RawMessage
			if err := d.Decode(&raw); err != nil {
				return err
			}
			if len(raw) == 0 || raw[0] != '{' {
				return nil
			}
			if err := sink.MarkCategory(Settings); err != nil {
				return err
			}
			values, _ := json.Marshal(map[string]json.RawMessage{"invidiousPreferences": raw})
			return write(&Record{Type: TypeSettings, Values: values})
		}
		return skip(d)
	})
}

func (invidiousAdapter) Encode(src Source, w io.Writer, cats []Category) error {
	j := &jsonWriter{w: w}
	ids := func(c Category, pick func(*Record) string) error {
		j.raw("[")
		n := 0
		var err error
		if hasCategory(cats, c) {
			err = src.ForEach(c, func(r *Record) error {
				id := pick(r)
				if id == "\x00" {
					return nil
				}
				if n > 0 {
					j.raw(",")
				}
				j.str(id)
				n++
				return j.err
			})
		}
		j.raw("]")
		return err
	}
	j.raw(`{"subscriptions":`)
	if err := ids(Subscriptions, func(r *Record) string { return youtubeID(r.ChannelURL) }); err != nil {
		return err
	}
	j.raw(`,"watch_history":`)
	if err := ids(History, func(r *Record) string {
		if r.Video == nil {
			return "\x00"
		}
		return youtubeID(r.Video.URL)
	}); err != nil {
		return err
	}
	j.raw(`,"preferences":{},"playlists":[`)
	if hasCategory(cats, Playlists) {
		var lists []*Record
		src.ForEach(Playlists, func(r *Record) error {
			if r.Type == TypePlaylist {
				lists = append(lists, r)
			}
			return nil
		})
		for i, p := range lists {
			if i > 0 {
				j.raw(",")
			}
			j.raw(`{"title":`)
			j.str(p.Name)
			j.raw(`,"description":`)
			j.str(p.Description)
			j.raw(`,"privacy":"Private","videos":[`)
			n := 0
			if err := src.ForEachChild(Playlists, p.SourceID, func(v *Record) error {
				if v.Type != TypePlaylistVideo || v.Video == nil {
					return nil
				}
				if n > 0 {
					j.raw(",")
				}
				j.str(youtubeID(v.Video.URL))
				n++
				return nil
			}); err != nil {
				return err
			}
			j.raw("]}")
		}
	}
	j.raw("]}")
	return j.err
}
