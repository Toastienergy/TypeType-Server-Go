package recommender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"typetype.dev/server/internal/youtube"
)

// HomePage is what HomeRecommendationsResponse carries when the home page comes from the recommender.
type HomePage struct {
	Items      []youtube.VideoItem
	NextCursor *string
	HasMore    bool
	Algorithm  string
	Rows       []Row
}

// Home is RecommenderHomeService: the home page of a user who turned the recommender on, from a kept page or one
// built within PageDeadline; nil sends the caller to the built-in recommendations.
type Home struct {
	cfg      Config
	admin    AdminSource
	settings *SettingsService
	catalog  *Catalog
	builder  *Builder
	store    *PageStore
	metrics  *Metrics
	turns    chan struct{}

	mu       sync.Mutex
	building map[string]*build
}

type build struct {
	done    chan struct{}
	variant int
	page    *Page
}

func NewHome(cfg Config, admin AdminSource, settings *SettingsService, catalog *Catalog, builder *Builder, store *PageStore, metrics *Metrics) *Home {
	return &Home{cfg: cfg, admin: admin, settings: settings, catalog: catalog, builder: builder, store: store, metrics: metrics,
		turns: make(chan struct{}, cfg.ParallelBuilds), building: map[string]*build{}}
}

func (h *Home) Get(ctx context.Context, user string, limit int, rawCursor, locale, timezone string) *HomePage {
	if !h.cfg.Installed() {
		return nil
	}
	admin, err := h.admin(ctx)
	if err != nil || !admin.Enabled {
		return nil
	}
	st, err := h.settings.Get(ctx, user)
	if err != nil || !st.Enabled {
		return nil
	}
	algorithm := ""
	if st.AlgorithmID != nil {
		algorithm = *st.AlgorithmID
	} else if algorithm = h.catalog.DefaultID(ctx, locale); algorithm == "" {
		return h.fallback()
	}
	cursor, hasCursor := DecodeCursor(rawCursor)
	var wanted int
	if hasCursor && cursor.Variant < h.cfg.PageVariants {
		wanted = cursor.Variant
	} else {
		wanted = h.store.NextVariant(ctx, user)
	}
	timeout := h.cfg.RequestTimeout
	if admin.TimeoutMs > 0 {
		timeout = time.Duration(admin.TimeoutMs) * time.Millisecond
	}
	request := func(variant int) PageRequest {
		return PageRequest{User: user, AlgorithmID: algorithm, Settings: st, Seed: h.store.SeedFor(variant), Locale: locale,
			Timezone: timezone, Timeout: timeout}
	}
	variant := wanted
	page := h.store.Get(ctx, user, algorithm, st, wanted)
	if page != nil && len(page.Items) == 0 {
		return h.fallback()
	}
	if page == nil && !hasCursor {
		type kept struct {
			variant int
			page    *Page
		}
		var others []kept
		for other := range h.cfg.PageVariants {
			if p := h.store.Get(ctx, user, algorithm, st, other); p != nil {
				others = append(others, kept{other, p})
			}
		}
		allEmpty := len(others) > 0
		for _, o := range others {
			if len(o.page.Items) > 0 {
				allEmpty = false
			}
		}
		if allEmpty {
			return h.fallback()
		}
		for _, o := range others {
			if len(o.page.Items) > 0 {
				h.start(request(wanted), wanted)
				variant, page = o.variant, o.page
				break
			}
		}
	}
	if page != nil {
		h.metrics.PagesFromCache.Add(1)
	} else {
		b := h.start(request(wanted), wanted)
		select {
		case <-b.done:
		case <-time.After(h.cfg.PageDeadline):
		case <-ctx.Done():
		}
		select {
		case <-b.done:
		default:
			return h.fallback()
		}
		if b.page == nil {
			return h.fallback()
		}
		variant, page = b.variant, b.page
	}
	offset := 0
	if hasCursor {
		offset = cursor.Offset
	}
	from := min(offset, len(page.Items))
	slice := page.Items[from:min(from+limit, len(page.Items))]
	hidden := h.store.Hidden(ctx, user)
	out := &HomePage{Items: []youtube.VideoItem{}, Algorithm: page.Algorithm, Rows: page.Rows}
	for _, v := range slice {
		if !hidden[v.URL] {
			out.Items = append(out.Items, v)
		}
	}
	if next := offset + len(slice); next < len(page.Items) {
		c := Cursor{variant, next}.Encode()
		out.NextCursor, out.HasMore = &c, true
	}
	if len(hidden) > 0 {
		rows := make([]Row, 0, len(page.Rows))
		for _, r := range page.Rows {
			var urls []string
			for _, u := range r.VideoURLs {
				if !hidden[u] {
					urls = append(urls, u)
				}
			}
			if urls == nil {
				urls = []string{}
			}
			r.VideoURLs = urls
			rows = append(rows, r)
		}
		out.Rows = rows
	}
	return out
}

func (h *Home) Forget(ctx context.Context, user string) { h.store.Forget(ctx, user) }

func (h *Home) HideVideo(ctx context.Context, user, videoURL string, hide bool) {
	h.store.Hide(ctx, user, videoURL, hide)
}

// Preparing reports a page being built for the user, which the built-in home announces.
func (h *Home) Preparing(user string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.building {
		if strings.HasPrefix(k, user+":") {
			return true
		}
	}
	return false
}

func (h *Home) LastTrace(ctx context.Context, user string) *Trace {
	return h.store.LastTrace(ctx, user)
}

// start builds one page per user, algorithm and settings at a time, ParallelBuilds at once for the instance; the
// build outlives the request that started it, so the next visit finds the page.
func (h *Home) start(req PageRequest, variant int) *build {
	sum := sha256.Sum256([]byte(req.Settings.storedJSON()))
	key := req.User + ":" + req.AlgorithmID + ":" + hex.EncodeToString(sum[:8])
	h.mu.Lock()
	if b, ok := h.building[key]; ok {
		h.mu.Unlock()
		return b
	}
	b := &build{done: make(chan struct{}), variant: variant}
	h.building[key] = b
	h.mu.Unlock()
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.building, key)
			h.mu.Unlock()
			close(b.done)
		}()
		h.turns <- struct{}{}
		defer func() { <-h.turns }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		started := time.Now()
		page := h.builder.Build(ctx, req)
		if page == nil {
			return
		}
		h.store.Put(ctx, req.User, req.Settings, variant, page)
		h.metrics.PagesBuilt.Add(1)
		h.metrics.BuildMillis.Add(time.Since(started).Milliseconds())
		if len(page.Items) > 0 {
			b.page = page
		}
	}()
	return b
}

func (h *Home) fallback() *HomePage {
	h.metrics.Fallbacks.Add(1)
	return nil
}
