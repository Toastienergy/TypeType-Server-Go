package recommender

import (
	"context"
	"regexp"
	"slices"
)

// DataItem and DataCategory are RecommenderDataItem and RecommenderDataCategory: what the recommender may read,
// as the data page shows it.
type DataItem struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Channel        *string `json:"channel"`
	At             int64   `json:"at"`
	WatchedPercent *int    `json:"watchedPercent"`
	Excluded       bool    `json:"excluded"`
	ThumbnailURL   *string `json:"thumbnailUrl"`
	Viewings       int     `json:"viewings"`
}

type DataCategory struct {
	ID            string     `json:"id"`
	Count         int        `json:"count"`
	OldestAt      *int64     `json:"oldestAt"`
	Use           bool       `json:"use"`
	RetentionDays *int       `json:"retentionDays"`
	Collect       *bool      `json:"collect"`
	Items         []DataItem `json:"items"`
}

// Dated is a subscription or a blocked entry with the date it was added.
type Dated struct {
	Ref   string
	Title string
	At    int64
}

// DataSources are the user's own lists the data page counts.
type DataSources struct {
	Subscriptions        func(ctx context.Context, user string) ([]Dated, error)
	BlockedOwn           func(ctx context.Context, user string) ([]Dated, error)
	WatchHistoryDisabled func(ctx context.Context, user string) bool
}

const (
	shownItems       = 50
	viewingsPerVideo = 4
)

var dataVideoIDRe = regexp.MustCompile(`(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})`)

// Overview is RecommenderDataService.overview.
func Overview(ctx context.Context, db Querier, src DataSources, feedback *FeedbackService, user string, st Settings) ([]DataCategory, error) {
	excluded := map[string]bool{}
	for _, e := range st.ExcludedItems {
		excluded[e] = true
	}
	recording := !src.WatchHistoryDisabled(ctx, user)
	category := func(id string, items []DataItem, count int, oldest int64, collect *bool) DataCategory {
		c := DataCategory{ID: id, Count: count, Use: !slices.Contains(st.UnusedData, id), Collect: collect, Items: items}
		if oldest > 0 {
			c.OldestAt = &oldest
		}
		if d, ok := st.RetentionDays[id]; ok {
			c.RetentionDays = &d
		}
		if c.Items == nil {
			c.Items = []DataItem{}
		}
		return c
	}
	watches := func(id string, shorts bool) (DataCategory, error) {
		cond := "url NOT LIKE '%/shorts/%'"
		if shorts {
			cond = "url LIKE '%/shorts/%'"
		}
		var count int
		var oldest *int64
		if err := db.QueryRow(ctx, "SELECT count(id), min(watched_at) FROM history WHERE user_id = $1 AND "+cond, user).Scan(&count, &oldest); err != nil {
			return DataCategory{}, err
		}
		rows, err := db.Query(ctx, `SELECT id, url, title, thumbnail, channel_name, duration, progress, watched_at FROM history
WHERE user_id = $1 AND `+cond+` ORDER BY watched_at DESC, id DESC LIMIT $2`, user, shownItems*viewingsPerVideo)
		if err != nil {
			return DataCategory{}, err
		}
		var order []string
		byURL := map[string][]HistoryItem{}
		for rows.Next() {
			var it HistoryItem
			if err := rows.Scan(&it.ID, &it.URL, &it.Title, &it.Thumbnail, &it.ChannelName, &it.Duration, &it.Progress, &it.WatchedAt); err != nil {
				rows.Close()
				return DataCategory{}, err
			}
			if _, ok := byURL[it.URL]; !ok {
				order = append(order, it.URL)
			}
			byURL[it.URL] = append(byURL[it.URL], it)
		}
		rows.Close()
		var items []DataItem
		for _, u := range order[:min(len(order), shownItems)] {
			viewings := byURL[u]
			newest := viewings[0]
			ref := videoRef(newest.URL)
			allExcluded := true
			var best *int
			for _, v := range viewings {
				if !excluded[historyRef(v)] {
					allExcluded = false
				}
				if v.Duration > 0 {
					p := int(min(max(v.Progress*100/v.Duration, 0), 100))
					if best == nil || p > *best {
						best = &p
					}
				}
			}
			channel := newest.ChannelName
			item := DataItem{ID: ref, Title: newest.Title, Channel: &channel, At: newest.WatchedAt, WatchedPercent: best,
				Excluded: excluded[ref] || allExcluded, Viewings: len(viewings)}
			if newest.Thumbnail != "" {
				item.ThumbnailURL = &newest.Thumbnail
			} else if m := dataVideoIDRe.FindStringSubmatch(newest.URL); m != nil {
				t := "https://i.ytimg.com/vi/" + m[1] + "/mqdefault.jpg"
				item.ThumbnailURL = &t
			}
			items = append(items, item)
		}
		var o int64
		if oldest != nil {
			o = *oldest
		}
		return category(id, items, count, o, &recording), nil
	}
	history, err := watches(CatHistory, false)
	if err != nil {
		return nil, err
	}
	shorts, err := watches(CatShortsHistory, true)
	if err != nil {
		return nil, err
	}
	var likeCount int
	var likeOldest *int64
	if err := db.QueryRow(ctx, "SELECT count(video_url), min(favorited_at) FROM favorites WHERE user_id = $1", user).Scan(&likeCount, &likeOldest); err != nil {
		return nil, err
	}
	likes, err := (&History{db: db}).likes(ctx, user, shownItems)
	if err != nil {
		return nil, err
	}
	var likeItems []DataItem
	for _, l := range likes {
		ref := favoriteRef(l)
		channel := l.ChannelName
		likeItems = append(likeItems, DataItem{ID: ref, Title: l.Title, Channel: &channel, At: l.FavoritedAt, Excluded: excluded[ref], Viewings: 1})
	}
	var lo int64
	if likeOldest != nil {
		lo = *likeOldest
	}
	subs, err := src.Subscriptions(ctx, user)
	if err != nil {
		return nil, err
	}
	blocked, err := src.BlockedOwn(ctx, user)
	if err != nil {
		return nil, err
	}
	notForMe, err := feedback.List(ctx, user)
	if err != nil {
		return nil, err
	}
	var nfm []DataItem
	var nfmOldest int64
	count := 0
	for _, f := range notForMe {
		if f.Kind != KindVideo {
			continue
		}
		if count == 0 || f.CreatedAt < nfmOldest {
			nfmOldest = f.CreatedAt
		}
		count++
		if len(nfm) < shownItems {
			title := ""
			if f.VideoURL != nil {
				title = *f.VideoURL
			}
			nfm = append(nfm, DataItem{ID: "feedback:" + f.ID, Title: title, At: f.CreatedAt, Viewings: 1})
		}
	}
	return []DataCategory{
		history, shorts,
		category(CatFavorites, likeItems, likeCount, lo, nil),
		dated(category, CatSubscriptions, "subscription:", subs),
		dated(category, CatBlocked, "blocked:", blocked),
		category(CatNotInterested, nfm, count, nfmOldest, nil),
	}, nil
}

func dated(category func(string, []DataItem, int, int64, *bool) DataCategory, id, prefix string, list []Dated) DataCategory {
	sorted := slices.Clone(list)
	slices.SortStableFunc(sorted, func(a, b Dated) int {
		switch {
		case a.At > b.At:
			return -1
		case a.At < b.At:
			return 1
		}
		return 0
	})
	var items []DataItem
	var oldest int64
	for i, d := range sorted {
		if i < shownItems {
			items = append(items, DataItem{ID: prefix + d.Ref, Title: d.Title, At: d.At, Viewings: 1})
		}
		if i == 0 || d.At < oldest {
			oldest = d.At
		}
	}
	return category(id, items, len(list), oldest, nil)
}
