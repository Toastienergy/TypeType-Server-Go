package recommender

import (
	"context"

	"typetype.dev/server/internal/store"
	"typetype.dev/server/internal/youtube"
)

// KnownVideos is RecommenderKnownVideos.find: the videos a "videos" source names, as the user's history keeps them,
// in the order asked; a URL never watched is left out.
func KnownVideos(ctx context.Context, db Querier, user string, urls []string) ([]youtube.VideoItem, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	found := map[string]youtube.VideoItem{}
	for from := 0; from < len(urls); from += 1000 {
		rows, err := db.Query(ctx, `SELECT url, title, thumbnail, channel_name, channel_url, channel_avatar, duration FROM history
WHERE user_id = $1 AND url = ANY($2)`, user, urls[from:min(from+1000, len(urls))])
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v youtube.VideoItem
			if err := rows.Scan(&v.URL, &v.Title, &v.ThumbnailURL, &v.UploaderName, &v.UploaderURL, &v.UploaderAvatarURL, &v.Duration); err != nil {
				rows.Close()
				return nil, err
			}
			v.ID = store.VideoID(v.URL)
			v.ViewCount, v.Uploaded = -1, -1
			v.StreamType = "video_stream"
			v.IsShortFormContent = isShortsURL(v.URL)
			found[v.URL] = v
		}
		rows.Close()
	}
	var out []youtube.VideoItem
	for _, u := range urls {
		if v, ok := found[u]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}
