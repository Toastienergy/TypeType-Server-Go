package portability

import (
	"archive/zip"
	"io"
	"sort"
	"strings"
)

// The /imports/youtube-takeout job API reads a Takeout with YoutubeTakeoutParserService, older and narrower than
// the portability adapter: CSV lists and the first HTML history page only.

type LegacySubscription struct {
	ChannelURL   string `json:"channelUrl"`
	Name         string `json:"name"`
	AvatarURL    string `json:"avatarUrl"`
	SubscribedAt int64  `json:"subscribedAt"`
}

type LegacyPlaylist struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Videos      []LegacyVideo `json:"videos"`
	VideoCount  int           `json:"videoCount"`
	CreatedAt   int64         `json:"createdAt"`
}

// LegacyVideo is PlaylistVideoItem.
type LegacyVideo struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	Thumbnail     string `json:"thumbnail"`
	Duration      int64  `json:"duration"`
	Position      int    `json:"position"`
	ChannelName   string `json:"channelName"`
	ChannelURL    string `json:"channelUrl"`
	ChannelAvatar string `json:"channelAvatar"`
	ViewCount     int64  `json:"viewCount"`
	AddedAt       int64  `json:"addedAt"`
	PublishedAt   int64  `json:"publishedAt"`
	WatchPosition int64  `json:"watchPosition"`
	Watched       bool   `json:"watched"`
}

type LegacyHistory struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	Thumbnail     string `json:"thumbnail"`
	ChannelName   string `json:"channelName"`
	ChannelURL    string `json:"channelUrl"`
	ChannelAvatar string `json:"channelAvatar"`
	Duration      int64  `json:"duration"`
	Progress      int64  `json:"progress"`
	WatchedAt     int64  `json:"watchedAt"`
}

type LegacyFavorite struct {
	VideoURL      string `json:"videoUrl"`
	FavoritedAt   int64  `json:"favoritedAt"`
	Title         string `json:"title"`
	Thumbnail     string `json:"thumbnail"`
	Duration      int64  `json:"duration"`
	ChannelName   string `json:"channelName"`
	ChannelURL    string `json:"channelUrl"`
	ChannelAvatar string `json:"channelAvatar"`
	ViewCount     int64  `json:"viewCount"`
	PublishedAt   int64  `json:"publishedAt"`
}

// LegacyParsed is YoutubeTakeoutParsedData; ItemKeys keeps the playlist keys in first-seen order.
type LegacyParsed struct {
	Subscriptions    []LegacySubscription
	Playlists        []LegacyPlaylist
	ItemKeys         []string
	Items            map[string][]LegacyVideo
	Favorites        []LegacyFavorite
	WatchLater       []LegacyVideo
	History          []LegacyHistory
	Warnings, Errors []string
}

type csvTable struct {
	header []string
	rows   [][]string
}

func readTable(f *zip.File) (csvTable, error) {
	var t csvTable
	err := zipCSV(f, func(h, r []string) error {
		t.header = h
		if len(t.rows) >= 100_000 {
			return invalid("CSV contains too many rows")
		}
		t.rows = append(t.rows, r)
		return nil
	})
	if t.header == nil {
		r, err2 := f.Open()
		if err2 == nil {
			takeoutCSV(r, func(h []string) { t.header = h }, func([]string) error { return nil })
			r.Close()
		}
	}
	return t, err
}

func (t csvTable) anyHeader(f func(string) bool) bool { return csvShape{t.header, nil}.anyHeader(f) }
func (t csvTable) anyValue(f func(string) bool) bool  { return csvShape{nil, t.rows}.anyValue(f) }

func legacyTextValue(v string) bool {
	t := strings.TrimSpace(v)
	if t == "" || looksLikeChannelID(t) || looksLikePlaylistID(t) || looksLikeLikelyVideoID(t) || containsChannelURL(t) || containsWatchURL(t) {
		return false
	}
	_, ok := takeoutDate(t)
	return !ok
}

// ParseLegacyTakeout is YoutubeTakeoutParserService.parse.
func ParseLegacyTakeout(path string) (*LegacyParsed, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, invalid("Invalid takeout archive")
	}
	defer z.Close()
	if len(z.File) > 10_000 {
		return nil, invalid("Archive contains too many files")
	}
	p := &LegacyParsed{Items: map[string][]LegacyVideo{}, Warnings: []string{}, Errors: []string{}}
	var subs, lists, items csvTable
	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > 4<<30 {
			return nil, invalid("Archive entry too large")
		}
		if !strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
			continue
		}
		t, err := readTable(f)
		if err != nil {
			return nil, err
		}
		if len(t.header) == 0 {
			continue
		}
		stem := f.Name[strings.LastIndexByte(f.Name, '/')+1:]
		if i := strings.LastIndexByte(stem, '.'); i >= 0 {
			stem = stem[:i]
		}
		hasVideoID := t.anyHeader(isVideoIDHeader)
		switch {
		case (t.anyHeader(isChannelIDHeader) || t.anyValue(looksLikeChannelID)) &&
			(t.anyHeader(isChannelURLHeader) || t.anyValue(containsChannelURL) || t.anyHeader(isChannelTitleHeader) && isSubscriptionText(f.Name)):
			if subs.header == nil {
				subs.header = t.header
			}
			subs.rows = append(subs.rows, t.rows...)
		case (t.anyHeader(isPlaylistIDHeader) || t.anyValue(looksLikePlaylistID)) && (t.anyHeader(isPlaylistTitleHeader) || t.anyValue(legacyTextValue)) &&
			!(hasVideoID || t.anyValue(looksLikeLikelyVideoID)):
			if lists.header == nil {
				lists.header = t.header
			}
			lists.rows = append(lists.rows, t.rows...)
		case !isPlaylistManifestName(stem) && (hasVideoID || t.anyValue(looksLikeVideoID)) &&
			(isPlaylistText(f.Name) || takeoutNorm(stem) == "playlist items" || t.anyHeader(isPlaylistIDHeader) || t.anyHeader(isPlaylistTitleHeader) ||
				len(t.header) <= 3 && (t.anyHeader(isAddedAtHeader) || t.anyValue(dated))):
			if t.anyHeader(isPlaylistIDHeader) || t.anyHeader(isPlaylistTitleHeader) {
				if items.header == nil {
					items.header = t.header
				}
				items.rows = append(items.rows, t.rows...)
			} else {
				key := firstNonBlank(systemPlaylist(stem), stem)
				if items.header == nil {
					items.header = append([]string{"playlist source key"}, t.header...)
				}
				for _, r := range t.rows {
					items.rows = append(items.rows, append([]string{key}, r...))
				}
			}
		default:
			p.Warnings = append(p.Warnings, "Unsupported CSV schema: "+f.Name)
		}
	}
	for _, r := range subs.rows {
		rec, ok := parseSubscriptionRow(subs.header, r)
		if !ok {
			continue
		}
		p.Subscriptions = append(p.Subscriptions, LegacySubscription{ChannelURL: rec.ChannelURL, Name: rec.Name, AvatarURL: rec.AvatarURL})
	}
	var playlists []LegacyPlaylist
	for _, r := range lists.rows {
		if pl, ok := parsePlaylistRow(lists.header, r); ok {
			playlists = append(playlists, LegacyPlaylist{ID: pl.id, Name: pl.name, Description: pl.description, Videos: []LegacyVideo{}})
		}
	}
	history, err := legacyHistory(z.File, p)
	if err != nil {
		return nil, err
	}
	byID := map[string]LegacyHistory{}
	for _, h := range history {
		if id := videoIDOf(h.URL); id != "" {
			byID[id] = h
		}
	}
	rawKeys, raw := []string{}, map[string][]LegacyVideo{}
	for _, r := range items.rows {
		if unavailableRow(zipRow(items.header, r)) {
			continue
		}
		key, it, ok := parsePlaylistItemRow(items.header, r)
		if !ok {
			p.Errors = append(p.Errors, "Invalid playlist item row")
			continue
		}
		v := enrich(LegacyVideo{URL: it.url, Title: it.title, Thumbnail: it.thumbnail, Duration: it.duration, Position: it.position,
			AddedAt: it.addedAt, PublishedAt: -1}, byID)
		if _, ok := raw[key]; !ok {
			rawKeys = append(rawKeys, key)
		}
		raw[key] = append(raw[key], v)
	}
	seenList := map[string]bool{}
	var deduped []LegacyPlaylist
	for _, pl := range playlists {
		k := firstNonBlank(pl.ID, pl.Name)
		if !seenList[k] {
			seenList[k] = true
			deduped = append(deduped, pl)
		}
	}
	for _, key := range rawKeys {
		resolved := resolvePlaylistKey(key, deduped)
		if _, ok := p.Items[resolved]; !ok {
			p.ItemKeys = append(p.ItemKeys, resolved)
		}
		p.Items[resolved] = append(p.Items[resolved], raw[key]...)
	}
	var favorites []LegacyFavorite
	watchSeen := map[string]bool{}
	for _, key := range p.ItemKeys {
		switch systemPlaylist(key) {
		case watchLaterKey:
			for _, v := range p.Items[key] {
				if !watchSeen[v.URL] {
					watchSeen[v.URL] = true
					p.WatchLater = append(p.WatchLater, v)
				}
			}
		case likedKey:
			for _, v := range p.Items[key] {
				favorites = append(favorites, LegacyFavorite{v.URL, v.AddedAt, v.Title, v.Thumbnail, v.Duration, v.ChannelName, v.ChannelURL, v.ChannelAvatar, v.ViewCount, v.PublishedAt})
			}
		}
	}
	signals := &signalSink{}
	for _, f := range z.File {
		if f.FileInfo().IsDir() || !isYoutubeHTML(f.Name) || isHistoryPath(f.Name) {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		html, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			return nil, err
		}
		if err := htmlSignals(string(html), false, signals); err != nil {
			return nil, err
		}
	}
	subSeen := map[string]bool{}
	var subs2 []LegacySubscription
	for _, s := range append(p.Subscriptions, signals.subscriptions()...) {
		if !subSeen[s.ChannelURL] {
			subSeen[s.ChannelURL] = true
			subs2 = append(subs2, s)
		}
	}
	p.Subscriptions = subs2
	favSeen := map[string]bool{}
	for _, f := range append(favorites, signals.favorites()...) {
		if !favSeen[f.VideoURL] {
			favSeen[f.VideoURL] = true
			f.Title = firstNonBlank(f.Title, titleFor(f.VideoURL))
			p.Favorites = append(p.Favorites, f)
		}
	}
	if len(p.Subscriptions) == 0 {
		p.Warnings = append(p.Warnings, "No subscription rows detected")
	}
	for _, pl := range deduped {
		if systemPlaylist(pl.Name) == "" && systemPlaylist(pl.ID) == "" {
			p.Playlists = append(p.Playlists, pl)
		}
	}
	for _, k := range p.ItemKeys {
		seen := map[string]bool{}
		var out []LegacyVideo
		for _, v := range p.Items[k] {
			if !seen[v.URL] {
				seen[v.URL] = true
				out = append(out, v)
			}
		}
		p.Items[k] = out
	}
	type hk struct {
		u  string
		at int64
	}
	hSeen := map[hk]bool{}
	for _, h := range history {
		if !hSeen[hk{h.URL, h.WatchedAt}] {
			hSeen[hk{h.URL, h.WatchedAt}] = true
			p.History = append(p.History, h)
		}
	}
	return p, nil
}

func enrich(v LegacyVideo, byID map[string]LegacyHistory) LegacyVideo {
	id := videoIDOf(v.URL)
	h, ok := byID[id]
	if !ok {
		h = LegacyHistory{}
	}
	v.Title = firstNonBlank(v.Title, h.Title, titleFor(v.URL))
	v.Thumbnail = firstNonBlank(v.Thumbnail, h.Thumbnail, thumbnailFor(v.URL))
	if v.Duration <= 0 {
		v.Duration = h.Duration
	}
	v.ChannelName, v.ChannelURL, v.ChannelAvatar = firstNonBlank(v.ChannelName, h.ChannelName), firstNonBlank(v.ChannelURL, h.ChannelURL), firstNonBlank(v.ChannelAvatar, h.ChannelAvatar)
	return v
}

// resolvePlaylistKey is YoutubeTakeoutPlaylistKeyResolver.resolve.
func resolvePlaylistKey(key string, lists []LegacyPlaylist) string {
	if s := systemPlaylist(key); s != "" {
		return s
	}
	n := takeoutNorm(key)
	for _, p := range lists {
		if takeoutNorm(p.ID) == n {
			return p.ID
		}
	}
	for _, p := range lists {
		if takeoutNorm(p.Name) == n {
			return p.Name
		}
	}
	sorted := append([]LegacyPlaylist{}, lists...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(takeoutNorm(sorted[i].Name)) > len(takeoutNorm(sorted[j].Name)) })
	for _, p := range sorted {
		name := takeoutNorm(p.Name)
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		if strings.HasSuffix(n, " "+name) || strings.HasPrefix(n, name+" ") || len(name) > 2 && strings.HasSuffix(n, name) {
			return p.Name
		}
	}
	return key
}

// legacyHistory reads the history page (or MyActivity, or the first YouTube page), whole, like the old parser.
func legacyHistory(files []*zip.File, p *LegacyParsed) ([]LegacyHistory, error) {
	var html []*zip.File
	for _, f := range files {
		if !f.FileInfo().IsDir() && isYoutubeHTML(f.Name) {
			html = append(html, f)
		}
	}
	var entry *zip.File
	for _, f := range html {
		if isHistoryPath(f.Name) {
			entry = f
			break
		}
	}
	if entry == nil {
		for _, f := range html {
			if strings.Contains(takeoutNorm(f.Name), "monactiv") {
				entry = f
				break
			}
		}
	}
	if entry == nil && len(html) > 0 {
		entry = html[0]
	}
	if entry == nil {
		return nil, nil
	}
	r, err := entry.Open()
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return nil, err
	}
	sink := &signalSink{}
	invalidDates, err := htmlHistory(string(body), !isHistoryPath(entry.Name), sink)
	if err != nil {
		return nil, err
	}
	var out []LegacyHistory
	for _, rec := range sink.records {
		v := rec.Video
		out = append(out, LegacyHistory{URL: v.URL, Title: firstNonBlank(v.Title, titleFor(v.URL)), Thumbnail: firstNonBlank(v.ThumbnailURL, thumbnailFor(v.URL)),
			ChannelName: v.ChannelName, ChannelURL: v.ChannelURL, WatchedAt: rec.WatchedAt})
	}
	if len(out) == 0 {
		p.Warnings = append(p.Warnings, "No watch history rows detected")
	}
	if invalidDates > 0 {
		p.Warnings = append(p.Warnings, "Skipped "+itoa64(invalidDates)+" watch history rows with invalid dates")
	}
	return out, nil
}

// signalSink collects records in memory for the legacy parser.
type signalSink struct{ records []*Record }

func (s *signalSink) MarkCategory(Category) error { return nil }
func (s *signalSink) Write(r *Record) (bool, error) {
	s.records = append(s.records, r)
	return true, nil
}
func (s *signalSink) Issue(Issue) error                      { return nil }
func (s *signalSink) PutLookup(string, string, string) error { return nil }
func (s *signalSink) Lookup(string, string) (string, error)  { return "", nil }

func (s *signalSink) subscriptions() []LegacySubscription {
	var out []LegacySubscription
	for _, r := range s.records {
		if r.Type == TypeSubscription {
			out = append(out, LegacySubscription{ChannelURL: r.ChannelURL, Name: r.Name, SubscribedAt: r.SubscribedAt})
		}
	}
	return out
}

func (s *signalSink) favorites() []LegacyFavorite {
	var out []LegacyFavorite
	for _, r := range s.records {
		if r.Type == TypeFavorite && r.Video != nil {
			v := r.Video
			out = append(out, LegacyFavorite{VideoURL: v.URL, FavoritedAt: r.FavoritedAt, Title: v.Title, ChannelName: v.ChannelName, ChannelURL: v.ChannelURL, PublishedAt: -1})
		}
	}
	return out
}

// SystemPlaylist names Watch later or Liked videos when v is one of them in any Takeout language.
func SystemPlaylist(v string) string { return systemPlaylist(v) }
