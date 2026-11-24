package portability

import (
	"encoding/xml"
	"io"
	"os"
	"regexp"
	"strings"
)

// opmlAdapter is OpmlPortabilityAdapter, also registered for SkyTube and youtube-local exports, which are OPML
// files the user names since nothing in them tells them apart.
type opmlAdapter struct{ format Format }

var opmlVersion = regexp.MustCompile(`(?i)<opml[^>]*version=["']([^"']+)`)

func (a opmlAdapter) Descriptor() Descriptor {
	return Descriptor{a.format, 1, []Capability{both(Subscriptions, Partial)}, "opml", "application/xml"}
}

func (a opmlAdapter) AutoDetect() bool { return a.format == OPML }

func (a opmlAdapter) Detect(in *Input) *Detection {
	if in.Archive != nil {
		return nil
	}
	p := strings.TrimLeft(string(in.Probe), " \t\r\n\ufeff")
	if !strings.HasPrefix(p, "<") || !strings.Contains(strings.ToLower(p), "<opml") {
		return nil
	}
	var v *string
	if m := opmlVersion.FindStringSubmatch(p); m != nil {
		v = &m[1]
	}
	return &Detection{a.format, v, 92, "OPML document root"}
}

func normalizeOPMLChannel(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	if m := opmlChannelID.FindStringSubmatch(v); m != nil {
		return youtubeChannelURL(m[1])
	}
	return youtubeChannelURL(v)
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (a opmlAdapter) Decode(in *Input, sink Sink) error {
	if err := sink.MarkCategory(Subscriptions); err != nil {
		return err
	}
	f, err := os.Open(in.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := xml.NewDecoder(f)
	d.Strict = false
	outlines := 0
	for {
		t, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return invalid("Invalid OPML document")
		}
		e, ok := t.(xml.StartElement)
		if !ok || e.Name.Local != "outline" {
			continue
		}
		if outlines >= MaxContainerRecords {
			return invalid("OPML contains too many outlines")
		}
		outlines++
		raw := attr(e, "xmlUrl")
		if strings.TrimSpace(raw) == "" {
			raw = attr(e, "url")
		}
		if ch := normalizeOPMLChannel(raw); strings.TrimSpace(ch) != "" {
			name := attr(e, "title")
			if strings.TrimSpace(name) == "" {
				name = attr(e, "text")
			}
			if _, err := sink.Write(&Record{Type: TypeSubscription, ChannelURL: ch, Name: name}); err != nil {
				return err
			}
		}
	}
}

func (a opmlAdapter) Encode(src Source, w io.Writer, cats []Category) error {
	if !hasCategory(cats, Subscriptions) {
		return invalid("OPML export requires subscriptions")
	}
	j := &jsonWriter{w: w}
	j.raw(`<?xml version="1.0" encoding="UTF-8"?><opml version="2.0"><head><title>TypeType subscriptions</title></head><body>`)
	err := src.ForEach(Subscriptions, func(r *Record) error {
		id := youtubeID(r.ChannelURL)
		name := r.Name
		if strings.TrimSpace(name) == "" {
			name = id
		}
		j.raw(`<outline text="` + xmlAttr.Replace(name) + `" title="` + xmlAttr.Replace(name) + `" type="rss" xmlUrl="` +
			xmlAttr.Replace("https://www.youtube.com/feeds/videos.xml?channel_id="+id) + `" htmlUrl="` + xmlAttr.Replace(r.ChannelURL) + `"/>`)
		return j.err
	})
	if err != nil {
		return err
	}
	j.raw("</body></opml>")
	return j.err
}

func hasCategory(cs []Category, c Category) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}
