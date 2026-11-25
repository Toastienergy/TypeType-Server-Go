package recommender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// PageStore is RecommenderPageStore: built pages per user, algorithm, settings and variant for PageTTL, the last
// trace, the videos hidden by a "not for me", and a generation that forgets every page at once.
type PageStore struct {
	cache Cache
	cfg   Config
	now   func() time.Time
}

const (
	generationTTL = 30 * 24 * time.Hour
	lastTraceTTL  = 24 * time.Hour
	hiddenTTL     = 24 * time.Hour
	maxHidden     = 500
)

func NewPageStore(cache Cache, cfg Config) *PageStore {
	return &PageStore{cache: cache, cfg: cfg, now: time.Now}
}

// NextVariant turns between the PageVariants pages of a window, so each visit shows another one.
func (p *PageStore) NextVariant(ctx context.Context, user string) int {
	turn, err := p.cache.Increment(ctx, "recommender:turn:v1:"+user, p.cfg.PageTTL)
	if err != nil {
		turn = 0
	}
	return int(turn % int64(p.cfg.PageVariants))
}

// SeedFor is the variant's seed in the current PageTTL window; Kotlin keeps its low 32 bits.
func (p *PageStore) SeedFor(variant int) int32 {
	window := p.now().UnixMilli() / (max(int64(p.cfg.PageTTL/time.Second), 1) * 1000)
	return int32(window*int64(p.cfg.PageVariants) + int64(variant))
}

func (p *PageStore) Get(ctx context.Context, user, algorithm string, st Settings, variant int) *Page {
	raw, ok, _ := p.cache.Get(ctx, p.key(ctx, user, algorithm, st, variant))
	if !ok {
		return nil
	}
	var page Page
	if json.Unmarshal([]byte(raw), &page) != nil {
		return nil
	}
	return &page
}

func (p *PageStore) Put(ctx context.Context, user string, st Settings, variant int, page *Page) {
	if b, err := json.Marshal(page); err == nil {
		p.cache.Set(ctx, p.key(ctx, user, page.Algorithm, st, variant), string(b), p.cfg.PageTTL)
	}
	if page.Trace != nil {
		if b, err := json.Marshal(page.Trace); err == nil {
			p.cache.Set(ctx, "recommender:last-trace:v1:"+user, string(b), lastTraceTTL)
		}
	}
}

func (p *PageStore) LastTrace(ctx context.Context, user string) *Trace {
	raw, ok, _ := p.cache.Get(ctx, "recommender:last-trace:v1:"+user)
	if !ok {
		return nil
	}
	var t Trace
	if json.Unmarshal([]byte(raw), &t) != nil {
		return nil
	}
	return &t
}

func (p *PageStore) Hidden(ctx context.Context, user string) map[string]bool {
	out := map[string]bool{}
	for _, u := range p.hiddenList(ctx, user) {
		out[u] = true
	}
	return out
}

func (p *PageStore) hiddenList(ctx context.Context, user string) []string {
	raw, ok, _ := p.cache.Get(ctx, "recommender:hidden:v1:"+user)
	if !ok {
		return nil
	}
	var urls []string
	json.Unmarshal([]byte(raw), &urls)
	return urls
}

// Hide adds or removes a video from the pages already built, until they expire.
func (p *PageStore) Hide(ctx context.Context, user, videoURL string, hide bool) {
	var urls []string
	for _, u := range p.hiddenList(ctx, user) {
		if u != videoURL {
			urls = append(urls, u)
		}
	}
	if hide {
		urls = append(urls, videoURL)
	}
	if len(urls) > maxHidden {
		urls = urls[len(urls)-maxHidden:]
	}
	if urls == nil {
		urls = []string{}
	}
	b, _ := json.Marshal(urls)
	p.cache.Set(ctx, "recommender:hidden:v1:"+user, string(b), hiddenTTL)
}

func (p *PageStore) Forget(ctx context.Context, user string) {
	p.cache.Increment(ctx, "recommender:generation:v1:"+user, generationTTL)
}

func (p *PageStore) key(ctx context.Context, user, algorithm string, st Settings, variant int) string {
	gen, ok, _ := p.cache.Get(ctx, "recommender:generation:v1:"+user)
	if !ok {
		gen = "0"
	}
	st.Enabled = true
	sum := sha256.Sum256([]byte(st.storedJSON()))
	return "recommender:page:v1:" + user + ":" + gen + ":" + algorithm + ":" + hex.EncodeToString(sum[:8]) + ":" +
		strconv.Itoa(int(p.SeedFor(variant)))
}

// Cursor is RecommenderPageCursor: "rec.<variant>.<offset>".
type Cursor struct{ Variant, Offset int }

func (c Cursor) Encode() string {
	return "rec." + strconv.Itoa(c.Variant) + "." + strconv.Itoa(c.Offset)
}

func IsCursor(raw string) bool { return strings.HasPrefix(raw, "rec.") }

func DecodeCursor(raw string) (Cursor, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != "rec" {
		return Cursor{}, false
	}
	v, err1 := strconv.Atoi(parts[1])
	o, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || v < 0 || o < 0 {
		return Cursor{}, false
	}
	return Cursor{v, o}, true
}
