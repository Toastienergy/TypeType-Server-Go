package recommender

import (
	"context"
	"encoding/json"
	"slices"
	"time"
)

// Cache is the part of Dragonfly the recommender uses; *cache.Cache satisfies it.
type Cache interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	Increment(ctx context.Context, key string, ttl time.Duration) (int64, error)
	DeleteMatching(ctx context.Context, pattern string) (int, error)
}

// Admin is the instance-wide recommender settings an admin chose.
type Admin struct {
	Enabled                 bool
	DefaultOn               bool
	DefaultAlgorithm        string
	AllowedAlgorithms       []string
	TimeoutMs               int
	StreamRequestsPerMinute int
}

// AdminSource reads the current admin settings.
type AdminSource func(ctx context.Context) (Admin, error)

// Catalog is RecommenderAlgorithmCatalog: the service's algorithms, kept for the settings lifetime, narrowed to
// the ones the admin allows, with the admin's default when it is offered.
type Catalog struct {
	client *Client
	cache  Cache
	cfg    Config
	admin  AdminSource
}

// AlgorithmName is RecommenderAlgorithmName.
type AlgorithmName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func NewCatalog(client *Client, cache Cache, cfg Config, admin AdminSource) *Catalog {
	return &Catalog{client: client, cache: cache, cfg: cfg, admin: admin}
}

func (c *Catalog) List(ctx context.Context, locale string) map[string]any {
	all := c.load(ctx, locale)
	if all == nil {
		return nil
	}
	st, _ := c.admin(ctx)
	var offered []any
	for _, a := range algorithms(all) {
		if len(st.AllowedAlgorithms) == 0 || slices.Contains(st.AllowedAlgorithms, id(a)) {
			offered = append(offered, a)
		}
	}
	out := make(map[string]any, len(all)+2)
	for k, v := range all {
		out[k] = v
	}
	if offered == nil {
		offered = []any{}
	}
	out["algorithms"] = offered
	out["default_algorithm"] = defaultOf(all, offered, st.DefaultAlgorithm)
	return out
}

func (c *Catalog) AllNames(ctx context.Context, locale string) []AlgorithmName {
	var out []AlgorithmName
	for _, a := range algorithms(c.load(ctx, locale)) {
		if i := id(a); i != "" {
			name, _ := a["name"].(string)
			if name == "" {
				name = i
			}
			out = append(out, AlgorithmName{i, name})
		}
	}
	return out
}

func (c *Catalog) OfferedIDs(ctx context.Context, locale string) []string {
	var out []string
	for _, a := range algorithms(c.List(ctx, locale)) {
		if i := id(a); i != "" {
			out = append(out, i)
		}
	}
	return out
}

// DataUsedBy lists what an offered algorithm reads (history, favorites...); nil when it is not offered.
func (c *Catalog) DataUsedBy(ctx context.Context, locale, algorithmID string) map[string]bool {
	for _, a := range algorithms(c.List(ctx, locale)) {
		if id(a) != algorithmID {
			continue
		}
		out := map[string]bool{}
		uses, _ := a["uses"].([]any)
		for _, u := range uses {
			if s, ok := u.(string); ok {
				out[s] = true
			}
		}
		return out
	}
	return nil
}

func (c *Catalog) DefaultID(ctx context.Context, locale string) string {
	d, _ := c.List(ctx, locale)["default_algorithm"].(string)
	return d
}

func defaultOf(all map[string]any, offered []any, adminDefault string) any {
	var ids []string
	for _, a := range offered {
		if i := id(a); i != "" {
			ids = append(ids, i)
		}
	}
	if adminDefault != "" && slices.Contains(ids, adminDefault) {
		return adminDefault
	}
	if d, _ := all["default_algorithm"].(string); d != "" && slices.Contains(ids, d) {
		return d
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return nil
}

func (c *Catalog) load(ctx context.Context, locale string) map[string]any {
	key := "recommender:algorithms:v1:" + locale
	if raw, ok, _ := c.cache.Get(ctx, key); ok {
		var cached map[string]any
		if json.Unmarshal([]byte(raw), &cached) == nil && cached != nil {
			return cached
		}
	}
	res := c.client.Algorithms(ctx, locale, c.cfg.RequestTimeout)
	if !res.OK() {
		return nil
	}
	if b, err := json.Marshal(res.Body); err == nil {
		c.cache.Set(ctx, key, string(b), c.cfg.SettingsTTL)
	}
	return res.Body
}

func algorithms(m map[string]any) []map[string]any {
	list, _ := m["algorithms"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		if o, ok := a.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

func id(a any) string {
	o, _ := a.(map[string]any)
	s, _ := o["id"].(string)
	return s
}
