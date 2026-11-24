package portability

import (
	"encoding/json"
	"io"
	"strings"
)

// materialiousAdapter is MaterialiousPortabilityAdapter: an OPML file or Invidious-style subscription JSON.
type materialiousAdapter struct{}

func (materialiousAdapter) Descriptor() Descriptor {
	return Descriptor{Materialious, 1, []Capability{both(Subscriptions, Partial)}, "json", "application/json"}
}

func (materialiousAdapter) AutoDetect() bool { return true }

func (materialiousAdapter) Detect(in *Input) *Detection {
	if in.Archive != nil {
		return nil
	}
	if (opmlAdapter{Materialious}).Detect(in) != nil {
		v := "opml"
		return &Detection{Materialious, &v, 82, "Materialious-compatible OPML"}
	}
	p := string(in.Probe)
	if !strings.HasPrefix(strings.TrimLeft(p, " \t\r\n"), "{") || !strings.Contains(p, `"subscriptions"`) || strings.Contains(p, `"app_version`) {
		return nil
	}
	confidence := 86
	if strings.Contains(strings.ToLower(in.Filename), "materialious") {
		confidence = 99
	}
	v := "invidious-subscriptions"
	return &Detection{Materialious, &v, confidence, "Materialious Invidious subscription JSON"}
}

func (a materialiousAdapter) Decode(in *Input, sink Sink) error {
	d := a.Detect(in)
	if d == nil {
		return invalid("Unsupported Materialious export")
	}
	if *d.FormatVersion == "opml" {
		return (opmlAdapter{Materialious}).Decode(in, sink)
	}
	return objectFields(in, func(field string, dec *json.Decoder) error {
		if field != "subscriptions" {
			return skip(dec)
		}
		ok, err := peekArray(dec)
		if err != nil || !ok {
			return err
		}
		if err := sink.MarkCategory(Subscriptions); err != nil {
			return err
		}
		count := 0
		for dec.More() {
			count++
			if count > MaxContainerRecords {
				return invalid("Materialious export contains too many subscriptions")
			}
			var v any
			if err := dec.Decode(&v); err != nil {
				return invalid("Invalid Materialious subscription")
			}
			if ch := youtubeChannelURL(scalarText(v)); ch != "" {
				if _, err := sink.Write(&Record{Type: TypeSubscription, ChannelURL: ch}); err != nil {
					return err
				}
			}
		}
		_, err = dec.Token()
		return err
	})
}

func (materialiousAdapter) Encode(src Source, w io.Writer, cats []Category) error {
	if !hasCategory(cats, Subscriptions) {
		return invalid("Materialious export requires subscriptions")
	}
	j := &jsonWriter{w: w}
	j.raw(`{"subscriptions":[`)
	n := 0
	err := src.ForEach(Subscriptions, func(r *Record) error {
		if n > 0 {
			j.raw(",")
		}
		j.str(youtubeID(r.ChannelURL))
		n++
		return j.err
	})
	if err != nil {
		return err
	}
	j.raw("]}")
	return j.err
}
