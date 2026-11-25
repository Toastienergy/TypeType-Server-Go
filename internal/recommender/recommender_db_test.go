package recommender

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"typetype.dev/server/internal/db"
)

// TestUserDataQueries runs the data page, the known videos, the profile pictures and the feedback against the real
// schema (TEST_DATABASE_URL).
func TestUserDataQueries(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	user := "recommender-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() {
		for _, table := range []string{"history", "favorites", "recommender_feedback", "recommender_user_settings"} {
			pool.Exec(ctx, "DELETE FROM "+table+" WHERE user_id = $1", user)
		}
	})
	watch := func(id, url string, progress, at int64) {
		if _, err := pool.Exec(ctx, `INSERT INTO history (id, user_id, url, title, thumbnail, channel_name, channel_url, channel_avatar, duration,
progress, watched_at) VALUES ($1, $2, $3, 'Title', '', 'Chan', 'https://www.youtube.com/channel/UCx', 'https://img/avatar', 200, $4, $5)`,
			user+id, user, url, progress, at); err != nil {
			t.Fatal(err)
		}
	}
	watch("a", "https://www.youtube.com/watch?v=aaaaaaaaaaa", 50, 1000)
	watch("b", "https://www.youtube.com/watch?v=aaaaaaaaaaa", 150, 2000)
	watch("c", "https://www.youtube.com/shorts/bbbbbbbbbbb", 10, 3000)
	if _, err := pool.Exec(ctx, "INSERT INTO favorites (user_id, video_url, favorited_at, title) VALUES ($1, 'https://www.youtube.com/watch?v=ccccccccccc', 500, 'Fav')", user); err != nil {
		t.Fatal(err)
	}
	cache, cfg := newMemCache(), testConfig("x")
	admin := func(context.Context) (Admin, error) { return Admin{Enabled: true}, nil }
	settings := NewSettingsService(pool, cache, cfg, admin)
	feedback := NewFeedbackService(pool, settings, func(context.Context) string { return "signals" })
	verdict, kind, u := verdictNotForMe, KindVideo, "https://www.youtube.com/watch?v=ddddddddddd"
	if saved, err := feedback.Add(ctx, user, Feedback{Kind: kind, Verdict: verdict, VideoURL: &u}); err != nil || saved == nil {
		t.Fatalf("feedback %v %v", saved, err)
	}
	src := DataSources{
		Subscriptions: func(context.Context, string) ([]Dated, error) {
			return []Dated{{"c1", "One", 10}, {"c2", "Two", 20}}, nil
		},
		BlockedOwn:           func(context.Context, string) ([]Dated, error) { return nil, nil },
		WatchHistoryDisabled: func(context.Context, string) bool { return false },
	}
	cats, err := Overview(ctx, pool, src, feedback, user, Settings{ExcludedItems: []string{"history:" + user + "a"}})
	if err != nil {
		t.Fatal(err)
	}
	history := cats[0]
	if history.ID != CatHistory || history.Count != 2 || len(history.Items) != 1 || history.Items[0].Viewings != 2 ||
		*history.Items[0].WatchedPercent != 75 || history.Items[0].Excluded || *history.OldestAt != 1000 ||
		*history.Items[0].ThumbnailURL != "https://i.ytimg.com/vi/aaaaaaaaaaa/mqdefault.jpg" {
		t.Fatalf("history %+v", history)
	}
	if cats[1].Count != 1 || cats[2].Count != 1 || cats[3].Items[0].ID != "subscription:c2" || *cats[3].OldestAt != 10 || cats[5].Count != 1 {
		t.Fatalf("categories %+v", cats)
	}
	known, err := KnownVideos(ctx, pool, user, []string{"https://www.youtube.com/shorts/bbbbbbbbbbb", "https://never"})
	if err != nil || len(known) != 1 || !known[0].IsShortFormContent || known[0].ID != "bbbbbbbbbbb" {
		t.Fatalf("known %+v %v", known, err)
	}
	profile := map[string]any{
		"groups":   []any{map[string]any{"id": "g", "from": []any{"history:" + user + "a", "favorite:https://f", "subscription:x"}}},
		"channels": []any{map[string]any{"id": "channel:UCx", "from": []any{"history:" + user + "b"}}},
	}
	out := Illustrate(ctx, pool, user, map[string]string{}, profile)
	examples := out["groups"].([]any)[0].(map[string]any)["examples"].([]any)
	if len(examples) != 2 || examples[0].(map[string]any)["title"] != "Title" {
		t.Fatalf("examples %v", examples)
	}
	if out["channels"].([]any)[0].(map[string]any)["avatarUrl"] != "https://img/avatar" {
		t.Fatalf("channels %v", out["channels"])
	}
}
