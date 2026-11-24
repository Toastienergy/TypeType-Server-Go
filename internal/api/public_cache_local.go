package api

import (
	"sync"
	"time"
)

// localCache keeps decoded public answers in memory for a few seconds in front of Dragonfly, so a hot video page
// skips the round trip and the JSON decoding. Values are shared: handlers copy before changing them.
type localCache struct {
	mu      sync.Mutex
	entries map[string]localEntry
}

type localEntry struct {
	value   any
	expires time.Time
}

const (
	localCacheTTL     = 15 * time.Second
	localCacheEntries = 512
)

var publicLocal = &localCache{entries: map[string]localEntry{}}

func (c *localCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.value, true
}

func (c *localCache) put(key string, v any, ttl time.Duration) {
	ttl = min(ttl, localCacheTTL)
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= localCacheEntries {
		now, oldest, at := time.Now(), "", time.Time{}
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			} else if oldest == "" || e.expires.Before(at) {
				oldest, at = k, e.expires
			}
		}
		if len(c.entries) >= localCacheEntries {
			delete(c.entries, oldest)
		}
	}
	c.entries[key] = localEntry{v, time.Now().Add(ttl)}
}
