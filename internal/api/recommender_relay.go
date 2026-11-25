package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"typetype.dev/server/internal/recommender"
	"typetype.dev/server/internal/store"
	"typetype.dev/server/internal/youtube"
)

// recRelay is RecommenderServices: the relay to the external recommender (TypeType-Signals) and what it reads of the
// server's own lookups and of the user's data.
type recRelay struct {
	cfg       recommender.Config
	client    *recommender.Client
	catalog   *recommender.Catalog
	settings  *recommender.SettingsService
	feedback  *recommender.FeedbackService
	snapshots *recommender.Snapshots
	home      *recommender.Home
	limiter   *recommender.Limiter
	metrics   *recommender.Metrics
}

func newRecRelay(s *Server) *recRelay {
	cfg := recommender.FromEnvironment()
	admin := s.recommenderAdmin
	m := &recommender.Metrics{}
	client := recommender.NewClient(cfg, &http.Client{})
	catalog := recommender.NewCatalog(client, s.Cache, cfg, admin)
	settings := recommender.NewSettingsService(s.DB, s.Cache, cfg, admin)
	feedback := recommender.NewFeedbackService(s.DB, settings, func(ctx context.Context) string { return catalog.DefaultID(ctx, "en") })
	limiter := recommender.NewLimiter(s.Cache, func(ctx context.Context) int {
		if a, err := admin(ctx); err == nil && a.StreamRequestsPerMinute > 0 {
			return a.StreamRequestsPerMinute
		}
		return cfg.StreamRequestsPerMinute
	}, cfg.StreamWallCooldown)
	snapshots := recommender.NewSnapshots(recommender.NewHistory(s.DB, client, s.Cache, cfg), s.recSubscriptions, s.recBlocked,
		feedback.NotForMeURLs, catalog.DataUsedBy)
	sources := recommender.NewSources(recommender.Loaders{
		Search: func(ctx context.Context, q string) []youtube.VideoItem {
			page, fail := s.cachedYoutubeSearch(ctx, q, defaultYoutubeSelection(), "")
			if fail != nil {
				return nil
			}
			return page.Items
		},
		ChannelVideos: func(ctx context.Context, channelURL, sort string) []youtube.VideoItem {
			ch, fail := s.cachedYoutubeChannel(ctx, channelURL, "", sort)
			if fail != nil || ch == nil {
				return nil
			}
			return ch.Videos
		},
		Trending: func(ctx context.Context) []youtube.VideoItem {
			videos, _ := s.YT.Trending(ctx)
			return videos
		},
		Related: s.relatedVideos,
		SubscriptionFeed: func(_ context.Context, user string) []youtube.VideoItem {
			return s.recs.subscriptionVideos(user, 1<<30, true)
		},
		KnownVideos: func(ctx context.Context, user string, urls []string) []youtube.VideoItem {
			videos, _ := recommender.KnownVideos(ctx, s.DB, user, urls)
			return videos
		},
	}, s.Cache, cfg, limiter, m)
	builder := recommender.NewBuilder(client, snapshots, sources, m)
	home := recommender.NewHome(cfg, admin, settings, catalog, builder, recommender.NewPageStore(s.Cache, cfg), m)
	if cfg.Installed() {
		s.YT.OnRefused(func() { go limiter.Opened(context.Background(), true, true) })
	}
	return &recRelay{cfg: cfg, client: client, catalog: catalog, settings: settings, feedback: feedback, snapshots: snapshots,
		home: home, limiter: limiter, metrics: m}
}

// streamOpened is StreamRequestWatch.opened: with the recommender installed, every video opened at YouTube counts
// against the limit the recommender's own requests stay under.
func (rr *recRelay) streamOpened(ctx context.Context, blocked bool) {
	if rr != nil && rr.cfg.Installed() {
		rr.limiter.Opened(ctx, false, blocked)
	}
}

func (s *Server) recommenderAdmin(ctx context.Context) (recommender.Admin, error) {
	st, err := s.Admin.Get(ctx)
	if err != nil {
		return recommender.Admin{}, err
	}
	a := recommender.Admin{Enabled: st.RecommenderEnabled, DefaultOn: st.RecommenderDefaultOn, AllowedAlgorithms: st.RecommenderAllowedAlgorithms,
		TimeoutMs: st.RecommenderTimeoutMs, StreamRequestsPerMinute: st.RecommenderStreamRequestsPerMinute}
	if st.RecommenderDefaultAlgorithm != nil {
		a.DefaultAlgorithm = *st.RecommenderDefaultAlgorithm
	}
	return a, nil
}

// offered is RecommenderServices.offered: installed, and turned on by the admin.
func (rr *recRelay) offered(ctx context.Context, admin recommender.AdminSource) bool {
	if !rr.cfg.Installed() {
		return false
	}
	a, err := admin(ctx)
	return err == nil && a.Enabled
}

func (rr *recRelay) requestTimeout(ctx context.Context, admin recommender.AdminSource) time.Duration {
	if a, err := admin(ctx); err == nil && a.TimeoutMs > 0 {
		return time.Duration(a.TimeoutMs) * time.Millisecond
	}
	return rr.cfg.RequestTimeout
}

func (s *Server) recSubscriptions(ctx context.Context, user string) ([]recommender.Subscription, error) {
	rows, err := s.DB.Query(ctx, "SELECT channel_url, name FROM subscriptions WHERE user_id = $1 ORDER BY subscribed_at DESC", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []recommender.Subscription
	for rows.Next() {
		var it recommender.Subscription
		if err := rows.Scan(&it.ChannelURL, &it.Name); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Server) recBlocked(ctx context.Context, user string) ([]string, []string, error) {
	var lists [2][]string
	for i, kind := range []string{"videos", "channels"} {
		items, err := s.blockedList(ctx, user, kind)
		if err != nil {
			return nil, nil, err
		}
		for _, it := range items {
			lists[i] = append(lists[i], it.URL)
		}
	}
	return lists[0], lists[1], nil
}

// relatedVideos is YoutubeRelatedVideosService behind CachedRelatedVideosService: the watch page's next response,
// kept five minutes; blocked reports YouTube's refusal, which pauses the recommender's own requests.
func (s *Server) relatedVideos(ctx context.Context, videoURL string) ([]youtube.VideoItem, bool) {
	id := store.VideoID(videoURL)
	if id == "" {
		return nil, false
	}
	area := cacheArea[[]youtube.VideoItem]{name: "related", admit: func(v []youtube.VideoItem) admission {
		return firstPageAdmission("", len(v) == 0, 300)
	}}
	videos, fail := publicCached(s, ctx, area, "youtube-related:v1:"+id, func(ctx context.Context) ([]youtube.VideoItem, *extractFail) {
		videos, err := s.YT.Related(ctx, id)
		switch {
		case refusedByYoutube(err):
			return nil, &extractFail{Status: http.StatusServiceUnavailable, Message: providerBlockedMessage, Code: "provider_access_blocked"}
		case err != nil:
			return nil, &extractFail{Status: http.StatusUnprocessableEntity, Message: "Could not load related videos", Code: "error"}
		}
		return videos, nil
	})
	if fail != nil {
		return nil, fail.Code == "provider_access_blocked"
	}
	return videos, false
}

func refusedByYoutube(err error) bool {
	return errors.Is(err, youtube.ErrStatus) && strings.Contains(err.Error(), " 429 ")
}

func defaultYoutubeSelection() searchSelection {
	content, _ := youtubeFilters.resolveContent("", false)
	sorts, _ := resolveSort(youtubeFilters.sortFor(content), nil)
	return searchSelection{content: content[0], sorts: sorts}
}

const (
	recMaxLocale   = 12
	recMaxTimezone = 64
)

// recLocale and recTimezone are recommenderLocale and recommenderTimezone.
func recLocale(r *http.Request) string {
	v := r.URL.Query().Get("locale")
	if !r.URL.Query().Has("locale") {
		v = r.Header.Get("Accept-Language")
		v, _, _ = strings.Cut(v, ",")
		v, _, _ = strings.Cut(v, ";")
	}
	v = strings.TrimSpace(v)
	if len(v) > recMaxLocale {
		v = v[:recMaxLocale]
	}
	if v == "" {
		return "en"
	}
	return v
}

func recTimezone(r *http.Request) string {
	v := strings.TrimSpace(r.URL.Query().Get("timezone"))
	if len(v) > recMaxTimezone {
		v = v[:recMaxTimezone]
	}
	if v == "" {
		return "UTC"
	}
	return v
}
