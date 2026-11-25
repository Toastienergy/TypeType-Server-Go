package youtube

import (
	"encoding/json/jsontext"
	"errors"
	"io"
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

// decodePruned decodes a JSON object as encoding/json does into map[string]any (numbers as float64), leaving out
// every member whose name is in skip, at any depth.
func decodePruned(r io.Reader, skip map[string]bool) (map[string]any, error) {
	d := jsontext.NewDecoder(r)
	v, err := prunedValue(d, skip)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("youtube: answer is not a JSON object")
	}
	return m, nil
}

func prunedValue(d *jsontext.Decoder, skip map[string]bool) (any, error) {
	tok, err := d.ReadToken()
	if err != nil {
		return nil, err
	}
	switch tok.Kind() {
	case '{':
		m := map[string]any{}
		for d.PeekKind() != '}' {
			name, err := d.ReadToken()
			if err != nil {
				return nil, err
			}
			key := name.String()
			if skip[key] {
				if err := d.SkipValue(); err != nil {
					return nil, err
				}
				continue
			}
			if m[key], err = prunedValue(d, skip); err != nil {
				return nil, err
			}
		}
		_, err = d.ReadToken()
		return m, err
	case '[':
		var list []any
		for d.PeekKind() != ']' {
			v, err := prunedValue(d, skip)
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
