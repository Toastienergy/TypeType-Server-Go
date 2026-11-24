package api

import (
	"container/list"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// startEntry is ProviderMediaStartupCache.Entry: the first bytes of a provider media file, or one segment of it at
// offset, and whether they are the whole file.
type startEntry struct {
	bytes    []byte
	total    int64
	ctype    string
	complete bool
	offset   int64
	stored   time.Time
	key      string
}

// startCache is ProviderMediaStartupCache: what the warmup fetched ahead of the player, 48 MiB at most, two minutes
// each, least recently used out first; a pending claim lets one fetch run per key while requests wait for it.
type startCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	pending map[string]chan struct{}
	bytes   int64
	max     int64
	ttl     time.Duration
}

func newStartCache() *startCache {
	return &startCache{entries: map[string]*list.Element{}, lru: list.New(), pending: map[string]chan struct{}{}, max: 48 << 20, ttl: 2 * time.Minute}
}

func segmentKey(url string, start int64) string { return url + "#" + strconv.FormatInt(start, 10) }

// begin claims key; extending allows a claim over an entry already stored, to replace it with a longer one.
func (c *startCache) begin(key string, extending bool) chan struct{} {
	if !extending && c.get(key) != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.pending[key]; busy {
		return nil
	}
	ch := make(chan struct{})
	c.pending[key] = ch
	return ch
}

func (c *startCache) finish(key string, claim chan struct{}) {
	c.mu.Lock()
	if c.pending[key] == claim {
		delete(c.pending, key)
	}
	c.mu.Unlock()
	select {
	case <-claim:
	default:
		close(claim)
	}
}

func (c *startCache) waitPending(key string, timeout time.Duration) {
	c.mu.Lock()
	ch := c.pending[key]
	c.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(timeout):
	}
}

func (c *startCache) isPending(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.pending[key]
	return ok
}

func (c *startCache) put(key string, e *startEntry) {
	if int64(len(e.bytes)) > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.bytes -= int64(len(el.Value.(*startEntry).bytes))
		c.lru.Remove(el)
	}
	e.key, e.stored = key, time.Now()
	c.entries[key] = c.lru.PushFront(e)
	c.bytes += int64(len(e.bytes))
	for c.bytes > c.max {
		old := c.lru.Back()
		oe := old.Value.(*startEntry)
		c.bytes -= int64(len(oe.bytes))
		delete(c.entries, oe.key)
		c.lru.Remove(old)
	}
}

func (c *startCache) get(key string) *startEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil
	}
	e := el.Value.(*startEntry)
	if time.Since(e.stored) > c.ttl {
		c.bytes -= int64(len(e.bytes))
		delete(c.entries, key)
		c.lru.Remove(el)
		return nil
	}
	c.lru.MoveToFront(el)
	return e
}

var byteRange = regexp.MustCompile(`^bytes=(\d+)-(\d*)$`)

// respond serves a request from the cache when the bytes it asks for are all there, as respondFrom does.
func (c *startCache) respond(url, rng string) *mediaReply {
	rng = strings.TrimSpace(rng)
	if e := c.get(url); e != nil {
		if m := e.reply(rng); m != nil {
			return m
		}
	}
	m := byteRange.FindStringSubmatch(rng)
	if m == nil {
		return nil
	}
	start, _ := strconv.ParseInt(m[1], 10, 64)
	if e := c.get(segmentKey(url, start)); e != nil {
		return e.reply(rng)
	}
	return nil
}

func (e *startEntry) reply(rng string) *mediaReply {
	rng = strings.TrimSpace(rng)
	if rng == "" {
		if !e.complete || e.offset != 0 {
			return nil
		}
		return &mediaReply{status: 200, ctype: e.ctype, length: strconv.Itoa(len(e.bytes)), accept: "bytes", text: e.bytes}
	}
	m := byteRange.FindStringSubmatch(rng)
	if m == nil {
		return nil
	}
	first, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return nil
	}
	end := e.offset + int64(len(e.bytes)) - 1
	last := end
	if m[2] != "" {
		if last, err = strconv.ParseInt(m[2], 10, 64); err != nil {
			return nil
		}
	} else if !e.complete {
		return nil
	}
	if first < e.offset || first > last || last > end {
		return nil
	}
	total := "*"
	if e.total > 0 {
		total = strconv.FormatInt(e.total, 10)
	}
	body := e.bytes[first-e.offset : last-e.offset+1]
	return &mediaReply{status: 206, ctype: e.ctype, length: strconv.Itoa(len(body)), accept: "bytes", text: body,
		crange: "bytes " + strconv.FormatInt(first, 10) + "-" + strconv.FormatInt(last, 10) + "/" + total}
}
