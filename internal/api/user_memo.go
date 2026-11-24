package api

import (
	"sync"
	"time"
)

// userMemo keeps each viewer's blocked lists, Focus profile and access profile for five seconds, so a cached page
// does not wait on three database reads. Any write request clears it, whoever sends it: admins change global
// rules and parents change their children's, so a write cannot be tied to one user.
type userMemo struct {
	mu  sync.Mutex
	gen uint64
	m   map[string]memoEntry
}

type memoEntry struct {
	gen   uint64
	until time.Time
	v     any
}

const (
	userMemoTTL     = 5 * time.Second
	userMemoEntries = 20_000
)

var userMemos = &userMemo{m: map[string]memoEntry{}}

func (u *userMemo) invalidate() {
	u.mu.Lock()
	u.gen++
	clear(u.m)
	u.mu.Unlock()
}

// memoized answers key from memory, or runs load; an answer whose load overlapped a write is not kept.
func memoized[T any](key string, load func() (T, bool)) T {
	u := userMemos
	u.mu.Lock()
	e, ok := u.m[key]
	gen := u.gen
	u.mu.Unlock()
	if ok && e.gen == gen && time.Now().Before(e.until) {
		if v, ok := e.v.(T); ok {
			return v
		}
	}
	v, keep := load()
	if !keep {
		return v
	}
	u.mu.Lock()
	if u.gen == gen {
		if len(u.m) >= userMemoEntries {
			clear(u.m)
		}
		u.m[key] = memoEntry{gen, time.Now().Add(userMemoTTL), v}
	}
	u.mu.Unlock()
	return v
}
