package recommender

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
)

const dayMs = 86_400_000

// Data categories a user can keep from the recommender, and the algorithm data each needs.
const (
	CatHistory       = "history"
	CatShortsHistory = "shorts_history"
	CatFavorites     = "favorites"
	CatSubscriptions = "subscriptions"
	CatBlocked       = "blocked"
	CatNotInterested = "not_interested"
)

// HistoryItem and FavoriteItem are the rows the snapshot and the history upload read.
type HistoryItem struct {
	ID, URL, Title, Thumbnail, ChannelName, ChannelURL string
	Duration, Progress, WatchedAt                      int64
}

type FavoriteItem struct {
	VideoURL, Title, ChannelName, ChannelURL string
	FavoritedAt                              int64
}

// DataFilter is RecommenderDataFilter: what the user lets the algorithm see, how far back, minus the items they
// excluded.
type DataFilter struct {
	settings Settings
	nowMs    int64
	uses     map[string]bool
	excluded map[string]bool
}

func NewDataFilter(st Settings, nowMs int64, algorithmUses map[string]bool) *DataFilter {
	ex := map[string]bool{}
	for _, e := range st.ExcludedItems {
		ex[e] = true
	}
	return &DataFilter{settings: st, nowMs: nowMs, uses: algorithmUses, excluded: ex}
}

func (f *DataFilter) Uses(category string) bool {
	if slices.Contains(f.settings.UnusedData, category) {
		return false
	}
	var need string
	switch category {
	case CatHistory, CatShortsHistory:
		need = "history"
	case CatFavorites:
		need = "favorites"
	case CatSubscriptions:
		need = "subscriptions"
	case CatBlocked, CatNotInterested:
		need = "not_interested"
	default:
		return false
	}
	return f.uses[need]
}

func (f *DataFilter) NeedsHistorySnapshot() bool {
	return f.Uses(CatHistory) || f.Uses(CatShortsHistory) || f.Uses(CatFavorites)
}

func (f *DataFilter) History(items []HistoryItem) []HistoryItem {
	var out []HistoryItem
	for _, it := range items {
		cat := CatHistory
		if isShortsURL(it.URL) {
			cat = CatShortsHistory
		}
		if f.Uses(cat) && f.Recent(cat, it.WatchedAt) && !f.excluded[historyRef(it)] && !f.excluded[videoRef(it.URL)] {
			out = append(out, it)
		}
	}
	return out
}

func (f *DataFilter) Favorites(items []FavoriteItem) []FavoriteItem {
	var out []FavoriteItem
	for _, it := range items {
		if f.Uses(CatFavorites) && f.Recent(CatFavorites, it.FavoritedAt) && !f.excluded[favoriteRef(it)] {
			out = append(out, it)
		}
	}
	return out
}

func (f *DataFilter) Recent(category string, at int64) bool {
	days := f.settings.RetentionDays[category]
	if days <= 0 {
		return true
	}
	return at <= 0 || at >= f.nowMs-int64(days)*dayMs
}

// Digest names what the filter keeps out, so a history sent under other choices is not reused; empty when nothing
// is kept out. The text hashed is Kotlin's (List and Map toString), so both servers name the same upload alike.
func (f *DataFilter) Digest() string {
	var cats []string
	for _, c := range []string{CatHistory, CatShortsHistory, CatFavorites} {
		if f.Uses(c) {
			cats = append(cats, c)
		}
	}
	if len(cats) == 0 {
		return ""
	}
	hasHistory := slices.Contains(cats, CatHistory) || slices.Contains(cats, CatShortsHistory)
	var exclusions []string
	for ref := range f.excluded {
		switch {
		case strings.HasPrefix(ref, "favorite:") && slices.Contains(cats, CatFavorites),
			(strings.HasPrefix(ref, "history:") || strings.HasPrefix(ref, "video:")) && hasHistory:
			exclusions = append(exclusions, ref)
		}
	}
	sort.Strings(exclusions)
	var limitKeys []string
	for c, d := range f.settings.RetentionDays {
		if slices.Contains(cats, c) && d > 0 {
			limitKeys = append(limitKeys, c)
		}
	}
	sort.Strings(limitKeys)
	if len(exclusions) == 0 && len(limitKeys) == 0 {
		return ""
	}
	day := int64(0)
	limits := make([]string, len(limitKeys))
	for i, c := range limitKeys {
		limits[i] = fmt.Sprintf("%s=%d", c, f.settings.RetentionDays[c])
	}
	if len(limitKeys) > 0 {
		day = f.nowMs / dayMs
	}
	text := "[" + strings.Join(cats, ", ") + "]|[" + strings.Join(exclusions, ", ") + "]|{" + strings.Join(limits, ", ") + "}|" +
		fmt.Sprint(day)
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

func historyRef(it HistoryItem) string   { return "history:" + it.ID }
func videoRef(url string) string         { return "video:" + url }
func favoriteRef(it FavoriteItem) string { return "favorite:" + it.VideoURL }

func isShortsURL(u string) bool { return strings.Contains(u, "/shorts/") }
