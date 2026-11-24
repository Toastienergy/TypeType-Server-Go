package portability

import (
	"archive/zip"
	"bufio"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// takeoutCSV is YoutubeTakeoutCsvReader: RFC 4180 quoting, CR, LF or CRLF rows, blank rows skipped, the first row
// the header with its byte-order mark removed, every field trimmed.
func takeoutCSV(r io.Reader, header func([]string), row func([]string) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var rec []string
	var cur strings.Builder
	quoted, hasData, seen := false, false, false
	emit := func() error {
		rec = append(rec, strings.TrimSpace(cur.String()))
		cur.Reset()
		blank := true
		for _, f := range rec {
			if strings.TrimSpace(f) != "" {
				blank = false
			}
		}
		defer func() { rec = rec[:0] }()
		if blank {
			return nil
		}
		if !seen {
			seen = true
			h := append([]string(nil), rec...)
			h[0] = strings.TrimPrefix(h[0], "\ufeff")
			header(h)
			return nil
		}
		return row(append([]string(nil), rec...))
	}
	for {
		c, _, err := br.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		hasData = true
		switch {
		case c == '"' && quoted:
			n, _, err := br.ReadRune()
			if err == nil && n == '"' {
				cur.WriteRune('"')
			} else {
				quoted = false
				if err == nil {
					br.UnreadRune()
				}
			}
		case c == '"':
			quoted = true
		case c == ',' && !quoted:
			rec = append(rec, strings.TrimSpace(cur.String()))
			cur.Reset()
		case (c == '\n' || c == '\r') && !quoted:
			if c == '\r' {
				if n, _, err := br.ReadRune(); err == nil && n != '\n' {
					br.UnreadRune()
				}
			}
			if err := emit(); err != nil {
				return err
			}
			hasData = false
		default:
			cur.WriteRune(c)
		}
		if cur.Len() > 2<<20 {
			return invalid("CSV field is too large")
		}
		if len(rec) > 256 {
			return invalid("CSV contains too many columns")
		}
	}
	if quoted {
		return invalid("CSV contains an unterminated quoted field")
	}
	if hasData || cur.Len() > 0 || len(rec) > 0 {
		return emit()
	}
	return nil
}

func zipCSV(f *zip.File, fn func(header, row []string) error) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	var header []string
	return takeoutCSV(r, func(h []string) { header = h }, func(row []string) error { return fn(header, row) })
}

type csvKind int

const (
	csvOther csvKind = iota
	csvManifest
	csvSubscriptions
	csvContent
)

type csvEntry struct {
	f        *zip.File
	kind     csvKind
	portable bool
}

func fileStem(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	return takeoutNorm(name)
}

var nonText = []string{"true", "false", "yes", "no", "oui", "non", "faux", "vrai", "public", "private", "prive", "publique", "privee",
	"unlisted", "non repertorie"}

func isTextValue(v string) bool {
	t := strings.TrimSpace(v)
	if t == "" || looksLikeChannelID(t) || looksLikePlaylistID(t) || looksLikeLikelyVideoID(t) || containsChannelURL(t) || containsWatchURL(t) {
		return false
	}
	if _, ok := takeoutDate(t); ok {
		return false
	}
	return !inSet(takeoutNorm(t), nonText)
}

// csvShape is YoutubeTakeoutCsvSchemaDetector's look at a header and the first 64 rows.
type csvShape struct {
	header []string
	rows   [][]string
}

func (s csvShape) anyHeader(f func(string) bool) bool {
	for _, h := range s.header {
		if f(h) {
			return true
		}
	}
	return false
}

func (s csvShape) anyValue(f func(string) bool) bool {
	for _, r := range s.rows {
		for _, v := range r {
			if f(v) {
				return true
			}
		}
	}
	return false
}

func dated(v string) bool { _, ok := takeoutDate(v); return ok }

func (s csvShape) channelID() bool {
	return s.anyHeader(isChannelIDHeader) || s.anyValue(looksLikeChannelID)
}
func (s csvShape) channelURL() bool {
	return s.anyValue(containsChannelURL) || len(s.rows) == 0 && s.anyHeader(isChannelURLHeader)
}
func (s csvShape) videoID() bool {
	return s.anyHeader(isVideoIDHeader) || s.anyValue(looksLikeLikelyVideoID)
}
func (s csvShape) playlistID() bool {
	return s.anyHeader(isPlaylistIDHeader) || s.anyValue(looksLikePlaylistID)
}
func (s csvShape) playlistKey() bool {
	return s.anyHeader(isPlaylistIDHeader) || s.anyHeader(isPlaylistTitleHeader)
}
func (s csvShape) addedAt() bool { return s.anyHeader(isAddedAtHeader) || s.anyValue(dated) }

func (s csvShape) subscription() bool {
	return s.channelID() && s.channelURL() && !s.videoID() && !s.playlistID()
}

func (s csvShape) manifest() bool {
	if !s.playlistID() || s.videoID() {
		return false
	}
	return s.anyHeader(isPlaylistTitleHeader) || s.anyValue(isTextValue)
}

func (s csvShape) content() bool {
	if !s.videoID() || s.channelID() {
		return false
	}
	return s.playlistKey() || len(s.header) <= 3 && (s.addedAt() || len(s.rows) > 0)
}

func (s csvShape) maybePortable() bool {
	return s.subscription() || s.manifest() || s.content() || s.channelURL() || s.playlistID() ||
		s.videoID() && !s.channelID() && len(s.header) <= 3
}

func inspect(f *zip.File) csvShape {
	var s csvShape
	r, err := f.Open()
	if err != nil {
		return s
	}
	defer r.Close()
	errStop := io.EOF
	takeoutCSV(r, func(h []string) { s.header = h }, func(row []string) error {
		if len(s.rows) >= 64 {
			return errStop
		}
		s.rows = append(s.rows, row)
		return nil
	})
	return s
}

func classifyCSV(files []*zip.File) []csvEntry {
	var out []csvEntry
	for _, f := range files {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".csv") || !strings.Contains(strings.ToLower(f.Name), "youtube") {
			continue
		}
		stem := fileStem(f.Name)
		switch {
		case isPlaylistManifestName(stem):
			out = append(out, csvEntry{f, csvManifest, false})
		case isSubscriptionText(stem):
			out = append(out, csvEntry{f, csvSubscriptions, false})
		case isPlaylistText(takeoutNorm(f.Name)):
			kind := csvContent
			if inspect(f).manifest() {
				kind = csvManifest
			}
			out = append(out, csvEntry{f, kind, false})
		default:
			s := inspect(f)
			kind := csvOther
			switch {
			case s.manifest():
				kind = csvManifest
			case s.subscription():
				kind = csvSubscriptions
			case s.content():
				kind = csvContent
			}
			out = append(out, csvEntry{f, kind, s.maybePortable()})
		}
	}
	return out
}

// rowValues is a header-to-value map in header order, as Kotlin's zip(...).toMap keeps it.
type rowValues struct {
	keys, vals []string
}

func zipRow(header, row []string) rowValues {
	var rv rowValues
	idx := map[string]int{}
	for i := 0; i < len(header) && i < len(row); i++ {
		if j, ok := idx[header[i]]; ok {
			rv.vals[j] = row[i]
			continue
		}
		idx[header[i]] = len(rv.keys)
		rv.keys, rv.vals = append(rv.keys, header[i]), append(rv.vals, row[i])
	}
	return rv
}

func (r rowValues) header(f func(string) bool) string {
	for i, k := range r.keys {
		if f(k) && strings.TrimSpace(r.vals[i]) != "" {
			return strings.TrimSpace(r.vals[i])
		}
	}
	return ""
}

func (r rowValues) value(f func(string) bool) string {
	for _, v := range r.vals {
		if f(v) && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (r rowValues) exact(keys ...string) string {
	norm := map[string]string{}
	for i, k := range r.keys {
		norm[takeoutNorm(k)] = strings.TrimSpace(r.vals[i])
	}
	for _, k := range keys {
		if v := norm[k]; v != "" {
			return v
		}
	}
	return ""
}

var isoDatePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

var metadataValues = []string{"true", "false", "public", "private", "prive", "publique", "unlisted", "non repertorie", "yes", "no", "oui", "non"}

func (r rowValues) firstText() string {
	return r.value(func(v string) bool {
		t := strings.TrimSpace(v)
		return t != "" && !looksLikeChannelID(t) && !looksLikePlaylistID(t) && !looksLikeLikelyVideoID(t) && !containsChannelURL(t) &&
			!containsWatchURL(t) && !inSet(takeoutNorm(t), metadataValues) && !isoDatePrefix.MatchString(t)
	})
}

func parseSubscriptionRow(header, row []string) (*Record, bool) {
	v := zipRow(header, row)
	id := firstNonBlank(v.header(isChannelIDHeader), v.value(looksLikeChannelID))
	u := v.header(isChannelURLHeader)
	if !containsChannelURL(u) {
		u = ""
	}
	u = firstNonBlank(u, v.value(containsChannelURL))
	if u == "" && id != "" {
		u = "https://www.youtube.com/channel/" + id
	}
	name := firstNonBlank(v.header(isChannelTitleHeader), v.firstText())
	if name == "" || strings.TrimSpace(u) == "" {
		return nil, false
	}
	return &Record{Type: TypeSubscription, ChannelURL: u, Name: name, AvatarURL: v.exact("thumbnail", "avatar")}, true
}

type takeoutPlaylist struct{ id, name, description string }

func parsePlaylistRow(header, row []string) (takeoutPlaylist, bool) {
	v := zipRow(header, row)
	title := firstNonBlank(v.header(isPlaylistTitleHeader), v.firstText())
	if title == "" {
		return takeoutPlaylist{}, false
	}
	return takeoutPlaylist{firstNonBlank(v.header(isPlaylistIDHeader), v.value(looksLikePlaylistID)), title, v.exact("description", "description de la playlist")}, true
}

type takeoutItem struct {
	url, title, thumbnail string
	duration              int64
	position              int
	addedAt               int64
}

func unavailableRow(v rowValues) bool {
	t := v.header(isVideoTitleHeader)
	return t != "" && isUnavailableTitle(t)
}

func parsePlaylistItemRow(header, row []string) (string, takeoutItem, bool) {
	v := zipRow(header, row)
	if unavailableRow(v) {
		return "", takeoutItem{}, false
	}
	key := firstNonBlank(v.exact("playlist source key"), v.header(isPlaylistIDHeader), v.header(isPlaylistTitleHeader), v.exact("playlist"))
	if key == "" {
		return "", takeoutItem{}, false
	}
	id := firstNonBlank(v.header(isVideoIDHeader), v.value(looksLikeVideoID))
	u := firstNonBlank(v.header(isURLHeader), v.value(containsWatchURL))
	if u == "" && id != "" {
		u = "https://www.youtube.com/watch?v=" + id
	}
	if strings.TrimSpace(u) == "" {
		return "", takeoutItem{}, false
	}
	it := takeoutItem{url: u, title: v.header(isVideoTitleHeader), thumbnail: v.exact("thumbnail")}
	it.duration, _ = strconv.ParseInt(v.exact("duration"), 10, 64)
	it.position, _ = strconv.Atoi(v.exact("position"))
	if at, ok := takeoutDate(v.header(isAddedAtHeader)); ok {
		it.addedAt = at
	} else if d := v.value(dated); d != "" {
		it.addedAt, _ = takeoutDate(d)
	}
	return key, it, true
}
