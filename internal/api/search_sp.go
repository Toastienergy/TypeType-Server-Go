package api

import (
	"encoding/base64"
	"net/url"
)

// featureFields are the Filters protobuf fields of YouTube's search features, in Features enum order.
var featureFields = [10]uint64{4, 5, 6, 7, 8, 9, 14, 15, 23, 25}

func protoVarint(b []byte, field, v uint64) []byte {
	b = appendUvarint(b, field<<3)
	return appendUvarint(b, v)
}

func appendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// youtubeSP is YoutubeSearchSortFilter.encodeSp: the sort order, then a Filters message (always present, since
// the feature list is never null), URL-encoded base64, with the "EgA=" special case PipePipe replaces.
func youtubeSP(content *filterItem, sorts []*filterItem) string {
	var sort, date, length *int
	var features [10]bool
	for _, it := range sorts {
		v := it.value
		switch it.field {
		case ytSort:
			sort = &v
		case ytDate:
			if v != 0 {
				date = &v
			}
		case ytLength:
			if v != 0 {
				length = &v
			}
		case ytFeature:
			features[v] = true
		}
	}
	var filters []byte
	if date != nil {
		filters = protoVarint(filters, 1, uint64(*date))
	}
	if content != nil && content.field == ytType && content.value != 0 {
		filters = protoVarint(filters, 2, uint64(content.value))
	}
	if length != nil {
		filters = protoVarint(filters, 3, uint64(*length))
	}
	for i, on := range features {
		if on {
			filters = protoVarint(filters, featureFields[i], 1)
		}
	}
	var req []byte
	if sort != nil {
		req = protoVarint(req, 1, uint64(*sort))
	}
	req = appendUvarint(req, 2<<3|2)
	req = appendUvarint(req, uint64(len(filters)))
	req = append(req, filters...)
	sp := url.QueryEscape(base64.StdEncoding.EncodeToString(req))
	if sp == "EgA%3D" {
		return "8AEB"
	}
	return sp
}
