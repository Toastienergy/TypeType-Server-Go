package api

import (
	"strconv"
	"strings"
)

// The search filter trees of PipePipe's YouTube, Bilibili and NicoNico search factories, with the identifiers its
// FilterGroup.Builder hands out in construction order, so values like "|2|channels" or "sortby|8|sort_publish_time"
// mean what they mean to every client.

// ytField says which part of YouTube's search protobuf an item sets.
type ytField byte

const (
	ytNone ytField = iota
	ytType
	ytSort
	ytDate
	ytLength
	ytFeature
	ytMusic
)

type filterItem struct {
	id   int
	name string
	// query is the provider URL parameter (Bilibili, NicoNico); field and value the YouTube protobuf setting, or
	// for YouTube Music the search params.
	query string
	field ytField
	value int
	music string
}

type filterGroup struct {
	id    int
	name  string
	one   bool
	items []*filterItem
}

type filterTree struct {
	content  []filterGroup
	all      []filterGroup
	variants map[int][]filterGroup
}

func (t *filterTree) sortFor(content []*filterItem) []filterGroup {
	if len(content) > 0 {
		if v, ok := t.variants[content[0].id]; ok {
			return v
		}
	}
	return t.all
}

func item(id int, name string) *filterItem { return &filterItem{id: id, name: name} }

func yt(id int, name string, f ytField, v int) *filterItem {
	return &filterItem{id: id, name: name, field: f, value: v}
}

func query(id int, name, q string) *filterItem { return &filterItem{id: id, name: name, query: q} }

var youtubeFilters = func() *filterTree {
	all, videos, channels, playlists := yt(0, "all", ytType, 0), yt(1, "videos", ytType, 1), yt(2, "channels", ytType, 2), yt(3, "playlists", ytType, 3)
	music := func(id int, name, params string) *filterItem {
		return &filterItem{id: id, name: name, field: ytMusic, music: params}
	}
	sortby := filterGroup{33, "sortby", true, []*filterItem{yt(11, "sort_relevance", ytSort, 0), yt(12, "sort_rating", ytSort, 1), yt(13, "sort_view", ytSort, 3)}}
	date := filterGroup{34, "upload_date", true, []*filterItem{yt(14, "all", ytDate, 0), yt(15, "past_hour", ytDate, 1), yt(16, "past_day", ytDate, 2),
		yt(17, "past_week", ytDate, 3), yt(18, "past_month", ytDate, 4), yt(19, "past_year", ytDate, 5)}}
	length := filterGroup{35, "duration", true, []*filterItem{yt(20, "all", ytLength, 0), yt(21, "short_video", ytLength, 1), yt(22, "long_video", ytLength, 2)}}
	var feats []*filterItem
	for i, n := range []string{"HD", "Subtitles", "Ccommons", "3d", "Live", "Purchased", "4k", "360°", "Location", "Hdr"} {
		feats = append(feats, yt(23+i, n, ytFeature, i))
	}
	features := filterGroup{36, "features", false, feats}
	video := []filterGroup{sortby, date, length, features}
	return &filterTree{
		content: []filterGroup{
			{4, "", true, []*filterItem{all, videos, channels, playlists}},
			{10, "YouTube Music", true, []*filterItem{
				music(5, "music_songs", "Eg-KAQwIARAAGAAgACgAMABqChAEEAUQAxAKEAk%3D"),
				music(6, "music_videos", "Eg-KAQwIABABGAAgACgAMABqChAEEAUQAxAKEAk%3D"),
				music(7, "music_albums", "Eg-KAQwIABAAGAEgACgAMABqChAEEAUQAxAKEAk%3D"),
				music(8, "music_playlists", "Eg-KAQwIABAAGAAgACgBMABqChAEEAUQAxAKEAk%3D"),
				music(9, "music_artists", "Eg-KAQwIABAAGAAgASgAMABqChAEEAUQAxAKEAk%3D"),
			}},
		},
		all:      video,
		variants: map[int][]filterGroup{0: video, 1: video, 2: {sortby}, 3: {sortby}},
	}
}()

var bilibiliFilters = func() *filterTree {
	video := []filterGroup{
		{17, "sortby", true, []*filterItem{query(6, "sort_overall", "order=totalrank"), query(8, "sort_publish_time", "order=pubdate"),
			query(7, "sort_view", "order=click"), query(9, "sort_bullet_comments", "order=dm"), query(10, "sort_comments", "order=scores"),
			query(11, "sort_bookmark", "order=stow")}},
		{18, "duration", true, []*filterItem{query(12, "all", "duration=0"), query(13, "short_video", "duration=1"),
			query(14, "medium_length", "duration=2"), query(15, "long_video", "duration=3"), query(16, "extra_long", "duration=4")}},
	}
	return &filterTree{
		content: []filterGroup{{5, "", true, []*filterItem{query(0, "videos", "search_type=video"), query(1, "lives", "search_type=live_room"),
			query(2, "channels", "search_type=bili_user"), query(3, "animes", "search_type=media_bangumi"), query(4, "movies_and_tv", "search_type=media_ft")}}},
		all:      video,
		variants: map[int][]filterGroup{0: video},
	}
}()

// niconicoAscending is the "sort_ascending" item, which turns order=d into order=a.
const niconicoAscending = 13

var niconicoFilters = func() *filterTree {
	popular, view, bookmark, likes := query(4, "sort_popular", "sort=h"), query(5, "sort_view", "sort=v"), query(6, "sort_bookmark", "sort=m"), query(7, "sort_likes", "sort=likeCount")
	published, commented := query(8, "sort_publish_time", "sort=f"), query(9, "sort_last_comment_time", "sort=n")
	plPopular, plCount, plRecent := query(10, "sort_popular", "sortKey=_hotTotalScore"), query(11, "sort_video_count", "sortKey=videoCount"), query(12, "sort_publish_time", "sortKey=startTime")
	duration := filterGroup{18, "duration", true, []*filterItem{query(14, "all", ""), query(15, "short_video", "l_range=1"), query(16, "long_video", "l_range=2")}}
	order := filterGroup{19, "sortorder", false, []*filterItem{item(niconicoAscending, "sort_ascending")}}
	return &filterTree{
		content: []filterGroup{{3, "", true, []*filterItem{item(0, "all"), item(1, "lives"), item(2, "playlists")}}},
		all: []filterGroup{{17, "sortby", true, []*filterItem{popular, view, bookmark, likes, published, commented, plPopular, plCount, plRecent}},
			duration, order},
		variants: map[int][]filterGroup{
			0: {{17, "sortby", true, []*filterItem{popular, view, bookmark, likes, published, commented}}, duration, order},
			2: {{17, "sortby", true, []*filterItem{plPopular, plCount, plRecent}}},
		},
	}
}()

func filtersFor(service int) *filterTree {
	switch service {
	case 0:
		return youtubeFilters
	case 5:
		return bilibiliFilters
	case 6:
		return niconicoFilters
	}
	return nil
}

type filterEntry struct {
	group *filterGroup
	item  *filterItem
}

func (e filterEntry) value() string {
	return e.group.name + "|" + strconv.Itoa(e.item.id) + "|" + e.item.name
}

func entries(groups []filterGroup) []filterEntry {
	var out []filterEntry
	for i := range groups {
		for _, it := range groups[i].items {
			out = append(out, filterEntry{&groups[i], it})
		}
	}
	return out
}

// resolveContent is resolveContentFilter: the first item of the first group when none is asked for.
func (t *filterTree) resolveContent(value string, given bool) ([]*filterItem, string) {
	if !given {
		return []*filterItem{t.content[0].items[0]}, ""
	}
	for _, e := range entries(t.content) {
		if e.value() == value {
			return []*filterItem{e.item}, ""
		}
	}
	return nil, "Unknown content filter"
}

// resolveSort is resolveSearchFilters, or defaultSearchFilters (the first item of every single-choice group)
// when none are asked for.
func resolveSort(groups []filterGroup, values []string) ([]*filterItem, string) {
	if len(values) == 0 {
		var out []*filterItem
		for _, g := range groups {
			if g.one && len(g.items) > 0 {
				out = append(out, g.items[0])
			}
		}
		return out, ""
	}
	want := map[string]bool{}
	for _, v := range values {
		want[v] = true
	}
	var selected []filterEntry
	for _, e := range entries(groups) {
		if want[e.value()] {
			selected = append(selected, e)
		}
	}
	if len(selected) != len(want) {
		return nil, "Unknown search filter"
	}
	count := map[int]int{}
	for _, e := range selected {
		count[e.group.id]++
		if e.group.one && count[e.group.id] > 1 {
			name := e.group.name
			if strings.TrimSpace(name) == "" {
				name = "filter"
			}
			return nil, "Only one '" + name + "' filter can be selected"
		}
	}
	out := make([]*filterItem, len(selected))
	for i, e := range selected {
		out[i] = e.item
	}
	return out, ""
}

type filterOption struct {
	Value     string `json:"value"`
	Label     string `json:"label"`
	IsDefault bool   `json:"isDefault"`
}

type filterGroupJSON struct {
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	MultiSelect bool           `json:"multiSelect"`
	Options     []filterOption `json:"options"`
}

func option(e filterEntry, isDefault, withGroup bool) filterOption {
	label := e.item.name
	if withGroup && strings.TrimSpace(e.group.name) != "" {
		label = e.group.name + ": " + e.item.name
	}
	return filterOption{Value: e.value(), Label: label, IsDefault: isDefault}
}

func (t *filterTree) response(content []*filterItem) map[string]any {
	contents := []filterOption{}
	for i, e := range entries(t.content) {
		contents = append(contents, option(e, i == 0, true))
	}
	groups := t.sortFor(content)
	sorts, out := []filterOption{}, []filterGroupJSON{}
	for i := range groups {
		g := &groups[i]
		j := filterGroupJSON{Key: g.name + "|" + strconv.Itoa(g.id), Label: g.name, MultiSelect: !g.one, Options: []filterOption{}}
		for k, it := range g.items {
			e := filterEntry{g, it}
			sorts = append(sorts, option(e, g.one && k == 0, true))
			j.Options = append(j.Options, option(e, g.one && k == 0, false))
		}
		out = append(out, j)
	}
	return map[string]any{"contentFilters": contents, "sortFilters": sorts, "filterGroups": out}
}
