package recommender

import (
	"context"
	"encoding/json"
	"strings"
)

// Profile is RecommenderProfileService.profile: what the service understood of the user, nil when unavailable.
func Profile(ctx context.Context, client *Client, snapshots *Snapshots, req PageRequest) map[string]any {
	snapshot := snapshots.Snapshot(ctx, req, false)
	if snapshot == nil {
		return nil
	}
	first := client.Profile(ctx, snapshot, req.Timeout)
	if first.OK() {
		return first.Body
	}
	if !first.Expired {
		return nil
	}
	again := snapshots.Snapshot(ctx, req, true)
	if again == nil {
		return nil
	}
	return client.Profile(ctx, again, req.Timeout).Body
}

const (
	examplesPerGroup  = 3
	watchesPerChannel = 1
)

// Illustrate is RecommenderProfileIllustrator.illustrate: up to three examples per group from the history or the
// favorites, and an avatar for each channel, from the subscriptions or else from a watch of that channel.
func Illustrate(ctx context.Context, db Querier, user string, followedAvatars map[string]string, profile map[string]any) map[string]any {
	groups, _ := profile["groups"].([]any)
	shown := make([][]string, len(groups))
	var watchIDs []string
	for i, g := range groups {
		for _, ref := range stringsOf(asMap(g)["from"]) {
			if len(shown[i]) == examplesPerGroup {
				break
			}
			shown[i] = append(shown[i], ref)
			if strings.HasPrefix(ref, "history:") {
				watchIDs = append(watchIDs, strings.TrimPrefix(ref, "history:"))
			}
		}
	}
	watches, err := watchesByID(ctx, db, user, watchIDs)
	if err != nil {
		return profile
	}
	followed := map[string]string{}
	for channelURL, avatar := range followedAvatars {
		if avatar != "" {
			followed[channelKey(channelURL)] = avatar
		}
	}
	channels, _ := profile["channels"].([]any)
	channelWatches := map[string][]string{}
	var unfollowed []string
	for _, c := range channels {
		id, _ := asMap(c)["id"].(string)
		var ids []string
		for _, ref := range stringsOf(asMap(c)["from"]) {
			if strings.HasPrefix(ref, "history:") && len(ids) < watchesPerChannel {
				ids = append(ids, strings.TrimPrefix(ref, "history:"))
			}
		}
		channelWatches[id] = ids
		if _, ok := followed[id]; !ok {
			unfollowed = append(unfollowed, ids...)
		}
	}
	watched, err := avatarsByWatchID(ctx, db, user, unfollowed)
	if err != nil {
		return profile
	}
	out := make(map[string]any, len(profile))
	for k, v := range profile {
		out[k] = v
	}
	newGroups := make([]any, len(groups))
	for i, g := range groups {
		ng := map[string]any{}
		for k, v := range asMap(g) {
			ng[k] = v
		}
		ng["examples"] = examples(shown[i], watches)
		newGroups[i] = ng
	}
	out["groups"] = newGroups
	newChannels := make([]any, len(channels))
	for i, c := range channels {
		id, _ := asMap(c)["id"].(string)
		avatar := followed[id]
		if avatar == "" {
			for _, w := range channelWatches[id] {
				if a := watched[w]; a != "" {
					avatar = a
					break
				}
			}
		}
		if avatar == "" {
			newChannels[i] = c
			continue
		}
		nc := map[string]any{}
		for k, v := range asMap(c) {
			nc[k] = v
		}
		nc["avatarUrl"] = avatar
		newChannels[i] = nc
	}
	out["channels"] = newChannels
	return out
}

func examples(refs []string, watches map[string]HistoryItem) []any {
	out := []any{}
	for _, ref := range refs {
		w, known := watches[ref]
		url := w.URL
		if !known {
			if !strings.HasPrefix(ref, "favorite:") {
				continue
			}
			url = strings.TrimPrefix(ref, "favorite:")
		}
		e := map[string]any{"url": url}
		if strings.TrimSpace(w.Title) != "" {
			e["title"] = w.Title
		}
		if strings.TrimSpace(w.ChannelName) != "" {
			e["channel"] = w.ChannelName
		}
		if strings.TrimSpace(w.Thumbnail) != "" {
			e["thumbnailUrl"] = w.Thumbnail
		} else if m := dataVideoIDRe.FindStringSubmatch(url); m != nil {
			e["thumbnailUrl"] = "https://i.ytimg.com/vi/" + m[1] + "/mqdefault.jpg"
		}
		out = append(out, e)
	}
	return out
}

func watchesByID(ctx context.Context, db Querier, user string, ids []string) (map[string]HistoryItem, error) {
	out := map[string]HistoryItem{}
	for from := 0; from < len(ids); from += 1000 {
		rows, err := db.Query(ctx, "SELECT id, url, title, thumbnail, channel_name FROM history WHERE user_id = $1 AND id = ANY($2)",
			user, ids[from:min(from+1000, len(ids))])
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var it HistoryItem
			if err := rows.Scan(&it.ID, &it.URL, &it.Title, &it.Thumbnail, &it.ChannelName); err != nil {
				rows.Close()
				return nil, err
			}
			out["history:"+it.ID] = it
		}
		rows.Close()
	}
	return out, nil
}

func avatarsByWatchID(ctx context.Context, db Querier, user string, ids []string) (map[string]string, error) {
	channelOf := map[string]string{}
	for from := 0; from < len(ids); from += 1000 {
		rows, err := db.Query(ctx, "SELECT id, channel_url FROM history WHERE user_id = $1 AND id = ANY($2)", user, ids[from:min(from+1000, len(ids))])
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, ch string
			if err := rows.Scan(&id, &ch); err != nil {
				rows.Close()
				return nil, err
			}
			channelOf[id] = ch
		}
		rows.Close()
	}
	var channels []string
	seen := map[string]bool{}
	for _, ch := range channelOf {
		if !seen[ch] {
			seen[ch] = true
			channels = append(channels, ch)
		}
	}
	avatarOf := map[string]string{}
	for from := 0; from < len(channels); from += 1000 {
		rows, err := db.Query(ctx, `SELECT DISTINCT channel_url, channel_avatar FROM history WHERE user_id = $1 AND channel_url = ANY($2)
AND channel_avatar <> ''`, user, channels[from:min(from+1000, len(channels))])
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ch, avatar string
			if err := rows.Scan(&ch, &avatar); err != nil {
				rows.Close()
				return nil, err
			}
			avatarOf[ch] = avatar
		}
		rows.Close()
	}
	out := map[string]string{}
	for id, ch := range channelOf {
		if a, ok := avatarOf[ch]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func channelKey(channelURL string) string {
	t := strings.TrimRight(channelURL, "/")
	return "channel:" + t[strings.LastIndex(t, "/")+1:]
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func stringsOf(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		switch s := x.(type) {
		case string:
			out = append(out, s)
		case float64, bool:
			b, _ := json.Marshal(s)
			out = append(out, string(b))
		}
	}
	return out
}
