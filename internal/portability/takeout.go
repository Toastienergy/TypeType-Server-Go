package portability

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"sort"
	"strings"
)

// takeoutAdapter is YoutubeTakeoutPortabilityAdapter: a Takeout archive (CSV lists, HTML or JSON activity) or one
// My Activity JSON file, import only.
type takeoutAdapter struct{}

func (takeoutAdapter) Descriptor() Descriptor {
	var caps []Capability
	for _, c := range []Category{Subscriptions, History, Playlists, WatchLater, Favorites} {
		caps = append(caps, importOnly(c, Complete))
	}
	return Descriptor{YoutubeTakout, 3, caps, "zip", "application/zip"}
}

func (takeoutAdapter) AutoDetect() bool { return true }

func (takeoutAdapter) Detect(in *Input) *Detection {
	if in.Archive == nil {
		if !isTakeoutJSON(in.Filename) {
			return nil
		}
		return &Detection{YoutubeTakout, nil, 90, "YouTube Takeout JSON activity file"}
	}
	files, root := 0, false
	for _, e := range in.Archive.Entries {
		n := strings.ToLower(e.Name)
		if strings.Contains(n, "youtube") && (strings.HasSuffix(n, ".csv") || strings.HasSuffix(n, ".html") || strings.HasSuffix(n, ".json")) {
			files++
		}
		if isTakeoutJSON(n) {
			files++
		}
		root = root || strings.HasPrefix(n, "takeout/")
	}
	if files == 0 {
		return nil
	}
	confidence := 90
	if root {
		confidence = 99
	}
	return &Detection{YoutubeTakout, nil, confidence, "YouTube Takeout CSV, HTML, or JSON activity files"}
}

func (a takeoutAdapter) Decode(in *Input, sink Sink) error {
	if a.Detect(in) == nil {
		return invalid("Unsupported YouTube Takeout archive")
	}
	if in.Archive == nil {
		f, err := os.Open(in.Path)
		if err != nil {
			return err
		}
		defer f.Close()
		return jsonActivities(f, sink, false)
	}
	z, err := zip.OpenReader(in.Path)
	if err != nil {
		return err
	}
	defer z.Close()
	var files []*zip.File
	for _, f := range z.File {
		if !f.FileInfo().IsDir() {
			files = append(files, f)
		}
	}
	csv := classifyCSV(files)
	listed := false
	for _, c := range csv {
		listed = listed || c.kind == csvSubscriptions
	}
	// Activity is read before the CSV lists so their subscriptions get the activity's dates.
	for _, f := range files {
		if !isYoutubeHTML(f.Name) {
			continue
		}
		history := isHistoryPath(f.Name)
		r, err := f.Open()
		if err != nil {
			return err
		}
		err = htmlWindows(r, func(html string) error {
			invalidDates, err := htmlHistory(html, !history, sink)
			if err != nil {
				return err
			}
			if invalidDates > 0 {
				if err := sink.Issue(Issue{History, "invalid_takeout_date", "YouTube Takeout HTML rows with an invalid date were skipped", invalidDates}); err != nil {
					return err
				}
			}
			if !history {
				return htmlSignals(html, listed, sink)
			}
			return nil
		})
		r.Close()
		if err != nil {
			return err
		}
	}
	for _, f := range files {
		if !isTakeoutJSON(f.Name) {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		err = jsonActivities(r, sink, listed)
		r.Close()
		if err != nil {
			return err
		}
	}
	return readTakeoutCSV(csv, sink)
}

func (takeoutAdapter) Encode(Source, io.Writer, []Category) error {
	return invalid("YouTube Takeout export is not available")
}

const playlistLookup = "youtube-takeout-playlist"

var playlistAliasFormats = []string{"Videos from %s", "Videos de %s", "Vídeos de %s", "Vidéos de %s", "Videos da playlist %s",
	"Vídeos da playlist %s", "Video di %s", "Video da %s", "Videos von %s", "Videos van %s", "Filmy z playlisty %s", "Видео из %s",
	"動画 - %s", "%s の動画", "%s 동영상", "%s 视频", "%s 影片", "%s videos"}

func playlistAliases(name string) []string {
	escaped := strings.NewReplacer("/", "_", "'", "_").Replace(name)
	out := []string{name, escaped + "-videos", escaped + "-vídeos"}
	for _, f := range playlistAliasFormats {
		out = append(out, strings.Replace(f, "%s", name, 1))
	}
	return out
}

func readTakeoutCSV(entries []csvEntry, sink Sink) error {
	write := func(r *Record) error {
		_, err := sink.Write(r)
		return err
	}
	invalidRow := func(c Category, kind string) error {
		return sink.Issue(Issue{c, "invalid_takeout_row", "An invalid YouTube Takeout " + kind + " row was skipped", 1})
	}
	for _, e := range entries {
		if e.kind != csvManifest {
			continue
		}
		if err := sink.MarkCategory(Playlists); err != nil {
			return err
		}
		err := zipCSV(e.f, func(header, row []string) error {
			p, ok := parsePlaylistRow(header, row)
			if !ok {
				return nil
			}
			resolved := firstNonBlank(systemPlaylist(p.id), systemPlaylist(p.name), firstNonBlank(p.id, p.name))
			if strings.TrimSpace(p.id) != "" {
				if err := sink.PutLookup(playlistLookup, p.id, resolved); err != nil {
					return err
				}
			}
			for _, alias := range playlistAliases(p.name) {
				if strings.TrimSpace(alias) == "" {
					continue
				}
				if err := sink.PutLookup(playlistLookup, alias, resolved); err != nil {
					return err
				}
			}
			if systemPlaylist(p.id) == "" && systemPlaylist(p.name) == "" {
				return write(&Record{Type: TypePlaylist, SourceID: p.id, Name: p.name, Description: p.description})
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, e := range entries {
		if e.kind != csvSubscriptions {
			continue
		}
		if err := sink.MarkCategory(Subscriptions); err != nil {
			return err
		}
		err := zipCSV(e.f, func(header, row []string) error {
			r, ok := parseSubscriptionRow(header, row)
			if !ok {
				return invalidRow(Subscriptions, "subscription")
			}
			if r.SubscribedAt <= 0 {
				r.SubscribedAt = subscribedAt(sink, r.ChannelURL)
			}
			return write(r)
		})
		if err != nil {
			return err
		}
	}
	for _, e := range entries {
		if e.kind != csvContent {
			continue
		}
		stem := e.f.Name[strings.LastIndexByte(e.f.Name, '/')+1:]
		if i := strings.LastIndexByte(stem, '.'); i >= 0 {
			stem = stem[:i]
		}
		inferred := ""
		if takeoutNorm(stem) != "playlist items" {
			inferred = firstNonBlank(systemPlaylist(stem), stem)
		}
		position := 0
		err := zipCSV(e.f, func(header, row []string) error {
			hasKey := false
			for _, h := range header {
				hasKey = hasKey || isPlaylistIDHeader(h) || isPlaylistTitleHeader(h)
			}
			if !hasKey && inferred != "" {
				header = append([]string{"playlist source key"}, header...)
				row = append([]string{inferred}, row...)
			}
			if unavailableRow(zipRow(header, row)) {
				return nil
			}
			key, item, ok := parsePlaylistItemRow(header, row)
			if !ok {
				return invalidRow(Playlists, "playlist item")
			}
			inferred = key
			if item.position <= 0 {
				item.position = position
			}
			position++
			return writePlaylistItem(key, item, sink)
		})
		if err != nil {
			return err
		}
	}
	for _, e := range entries {
		if e.kind == csvOther && e.portable && (isSubscriptionText(e.f.Name) || isPlaylistText(e.f.Name)) {
			if err := sink.Issue(Issue{"", "unsupported_takeout_csv", "A YouTube Takeout CSV file could not be classified and was skipped: " + e.f.Name, 1}); err != nil {
				return err
			}
		}
	}
	return nil
}

func writePlaylistItem(key string, it takeoutItem, sink Sink) error {
	v := &Video{URL: it.url, Title: firstNonBlank(it.title, titleFor(it.url)), ThumbnailURL: firstNonBlank(it.thumbnail, thumbnailFor(it.url)),
		DurationSeconds: it.duration, PublishedAt: -1}
	resolved := systemPlaylist(key)
	if resolved == "" {
		var err error
		if resolved, err = sink.Lookup(playlistLookup, key); err != nil {
			return err
		}
	}
	var err error
	switch resolved {
	case watchLaterKey:
		_, err = sink.Write(&Record{Type: TypeWatchLater, Video: v, AddedAt: it.addedAt})
	case likedKey:
		_, err = sink.Write(&Record{Type: TypeFavorite, Video: v, FavoritedAt: it.addedAt})
	case "":
		id := strings.TrimSpace(key)
		if _, err = sink.Write(&Record{Type: TypePlaylist, SourceID: id, Name: id}); err == nil {
			if _, err = sink.Write(&Record{Type: TypePlaylistVideo, PlaylistSourceID: id, Position: it.position, Video: v, AddedAt: it.addedAt}); err == nil {
				err = sink.Issue(Issue{Playlists, "playlist_manifest_missing", "A playlist was reconstructed from its item file", 1})
			}
		}
	default:
		_, err = sink.Write(&Record{Type: TypePlaylistVideo, PlaylistSourceID: resolved, Position: it.position, Video: v, AddedAt: it.addedAt})
	}
	return err
}

// sliceSource is PortabilityBatchRecordSource: a batch of one category's records.
type sliceSource struct {
	c    Category
	recs []*Record
}

func (s sliceSource) Counts() (map[Category]int64, error) {
	return map[Category]int64{s.c: int64(len(s.recs))}, nil
}

func (s sliceSource) ForEach(c Category, fn func(*Record) error) error {
	if c != s.c {
		return nil
	}
	for _, r := range s.recs {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

func (s sliceSource) ForEachChild(c Category, parent string, fn func(*Record) error) error {
	return s.ForEach(c, func(r *Record) error {
		if r.ParentKey() == lower(parent) {
			return fn(r)
		}
		return nil
	})
}

// TakeoutImport is YoutubeTakeoutImportWorkflow: subscriptions in batches of 50, the 20 latest views, playlists,
// the rest of the history in batches of 500, then the other categories, each batch committed on its own so a
// long import shows up in the apps as it goes.
func TakeoutImport(ctx context.Context, e *Engine, j *Job, req ImportRequest, counts map[Category]int64, r *reporter) (map[string]int64, error) {
	result := map[string]int64{}
	started := map[Category]bool{}
	publish := func() {
		copied := map[string]int64{}
		for k, v := range result {
			copied[k] = v
		}
		j.update(func(s *Snapshot) { s.Result = copied })
	}
	commit := func(c Category, recs []*Record, stage string, total int64) error {
		r.setStage(stage, c, &total)
		policy := req.DuplicatePolicy
		if started[c] {
			policy = "skip"
		}
		started[c] = true
		res, err := e.Port.Import(ctx, j.Owner, sliceSource{c, recs}, ImportRequest{[]Category{c}, policy},
			func(_ Category, n int64) error { return r.add(n) }, func(Category, int64) error { return nil })
		if err != nil {
			return err
		}
		result[string(c)] += res[string(c)]
		publish()
		return r.checkpoint()
	}
	paged := func(c Category, size int, include func(*Record) bool, fn func([]*Record) error) (int64, error) {
		var pending []*Record
		var matched int64
		err := j.spool.ForEach(c, func(rec *Record) error {
			if err := r.active(); err != nil {
				return err
			}
			if !include(rec) {
				return nil
			}
			matched++
			pending = append(pending, rec)
			if len(pending) == size {
				batch := pending
				pending = nil
				return fn(batch)
			}
			return nil
		})
		if err == nil && len(pending) > 0 {
			err = fn(pending)
		}
		return matched, err
	}
	selected := map[Category]bool{}
	for _, c := range req.Categories {
		selected[c] = true
	}
	var recent map[string]bool
	var remaining int64
	if selected[Subscriptions] {
		total := counts[Subscriptions]
		n, err := paged(Subscriptions, 50, func(*Record) bool { return true }, func(b []*Record) error { return commit(Subscriptions, b, "subscriptions", total) })
		if err == nil && n == 0 {
			err = commit(Subscriptions, nil, "subscriptions", total)
		}
		if err != nil {
			return nil, err
		}
	}
	if selected[History] {
		var all []*Record
		if err := j.spool.ForEach(History, func(rec *Record) error {
			if rec.Type == TypeHistory {
				all = append(all, rec)
				if len(all) > 40 {
					sortHistory(all)
					all = all[:20]
				}
			}
			return r.active()
		}); err != nil {
			return nil, err
		}
		sortHistory(all)
		if len(all) > 20 {
			all = all[:20]
		}
		recent = map[string]bool{}
		for _, rec := range all {
			recent[rec.StableKey()] = true
		}
		if err := commit(History, all, "recentHistory", int64(len(all))); err != nil {
			return nil, err
		}
		remaining = max(counts[History]-int64(len(all)), 0)
	}
	if selected[Playlists] {
		total := counts[Playlists]
		do := func(b []*Record) error { return commit(Playlists, b, "playlists", total) }
		n1, err := paged(Playlists, 5, func(x *Record) bool { return x.Type == TypePlaylist }, do)
		var n2, n3 int64
		if err == nil {
			n2, err = paged(Playlists, 50, func(x *Record) bool { return x.Type == TypePlaylistVideo }, do)
		}
		if err == nil {
			n3, err = paged(Playlists, 50, func(x *Record) bool { return x.Type != TypePlaylist && x.Type != TypePlaylistVideo }, do)
		}
		if err == nil && n1+n2+n3 == 0 {
			err = commit(Playlists, nil, "playlists", total)
		}
		if err != nil {
			return nil, err
		}
	}
	if selected[History] && remaining > 0 {
		if _, err := paged(History, 500, func(x *Record) bool { return x.Type != TypeHistory || !recent[x.StableKey()] },
			func(b []*Record) error { return commit(History, b, "history", remaining) }); err != nil {
			return nil, err
		}
	}
	for _, c := range req.Categories {
		if c == Subscriptions || c == History || c == Playlists {
			continue
		}
		total := counts[c]
		r.setStage("remaining", c, &total)
		_, err := e.Port.Import(ctx, j.Owner, j.spool, ImportRequest{[]Category{c}, req.DuplicatePolicy},
			func(_ Category, n int64) error { return r.add(n) },
			func(_ Category, n int64) error {
				result[string(c)] = n
				publish()
				return r.checkpoint()
			})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// sortHistory orders the latest views first, ties by stable key.
func sortHistory(rs []*Record) {
	sort.SliceStable(rs, func(a, b int) bool {
		if rs[a].WatchedAt != rs[b].WatchedAt {
			return rs[a].WatchedAt > rs[b].WatchedAt
		}
		return rs[a].StableKey() < rs[b].StableKey()
	})
}
