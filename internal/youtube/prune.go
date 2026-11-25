package youtube

import (
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"
	"sync"
)

// channelSkip lists containers of a channel browse answer that no parser reads. A videos tab is about 480 KB, and
// these menus, palettes, hover overlays and logging blobs are about 60% of it; skipping them at the token level
// spares the maps and strings that made the subscription feed rebuild the biggest allocator of the server.
var channelSkip = map[string]bool{
	"menuButton": true, "themedPalette": true, "thumbnailHoverOverlayToggleActionsViewModel": true,
	"signalServiceEndpoint": true, "videoCommand": true, "clickTrackingParams": true, "trackingParams": true,
	"watchEndpointSupportedOnesieConfig": true, "loggingContext": true, "loggingDirectives": true, "topbar": true,
	"accessibilityContext": true, "frameworkUpdates": true,
}

// nextSkip is channelSkip for a watch page's next answer (about 780 KB), plus its description panel copy, end
// screen overlays, merchandise shelf, consent interstitial and language list, which no parser reads either.
// frameworkUpdates stays: comments read their entities there.
var nextSkip = map[string]bool{
	"menuButton": true, "themedPalette": true, "thumbnailHoverOverlayToggleActionsViewModel": true,
	"signalServiceEndpoint": true, "videoCommand": true, "clickTrackingParams": true, "trackingParams": true,
	"watchEndpointSupportedOnesieConfig": true, "loggingContext": true, "loggingDirectives": true, "topbar": true,
	"accessibilityContext": true, "structuredDescriptionContentRenderer": true, "showSheetCommand": true,
	"playerOverlays": true, "merchandiseShelfRenderer": true, "interstitial": true, "languageList": true,
}

// decoders keep their read buffer between answers; duplicate member names are allowed, as encoding/json does
// (the last one wins), which also spares a name set per object.
var decoders = sync.Pool{New: func() any { return jsontext.NewDecoder(noInput) }}

var noInput = strings.NewReader("")

// decodePruned decodes a JSON object as encoding/json does into map[string]any (numbers as float64), leaving out
// every member whose name is in skip, at any depth.
func decodePruned(r io.Reader, skip map[string]bool) (map[string]any, error) {
	d := decoders.Get().(*jsontext.Decoder)
	d.Reset(r, jsontext.AllowDuplicateNames(true))
	defer func() {
		d.Reset(noInput)
		decoders.Put(d)
	}()
	v, err := (&pruner{d: d, skip: skip, names: map[string]string{}}).value()
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("youtube: answer is not a JSON object")
	}
	return m, nil
}

// pruner reads one answer; names keeps each member name once, since the same few hundred names repeat
// thousands of times in a page.
type pruner struct {
	d     *jsontext.Decoder
	skip  map[string]bool
	names map[string]string
}

func (p *pruner) name() (string, error) {
	raw, err := p.d.ReadValue()
	if err != nil {
		return "", err
	}
	if n, ok := p.names[string(raw)]; ok {
		return n, nil
	}
	b, err := jsontext.AppendUnquote(nil, raw)
	if err != nil {
		return "", err
	}
	n := string(b)
	p.names[string(raw)] = n
	return n, nil
}

func (p *pruner) value() (any, error) {
	d := p.d
	tok, err := d.ReadToken()
	if err != nil {
		return nil, err
	}
	switch tok.Kind() {
	case '{':
		m := map[string]any{}
		for d.PeekKind() != '}' {
			key, err := p.name()
			if err != nil {
				return nil, err
			}
			if p.skip[key] {
				if err := d.SkipValue(); err != nil {
					return nil, err
				}
				continue
			}
			if m[key], err = p.value(); err != nil {
				return nil, err
			}
		}
		_, err = d.ReadToken()
		return m, err
	case '[':
		var list []any
		for d.PeekKind() != ']' {
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		if list == nil {
			list = []any{}
		}
		_, err = d.ReadToken()
		return list, err
	case '"':
		return tok.String(), nil
	case '0':
		return tok.Float()
	case 't', 'f':
		return tok.Bool(), nil
	default:
		return nil, nil
	}
}
