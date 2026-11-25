package recommender

import (
	"context"
	"strconv"
	"time"
)

// Limiter is RecommenderStreamLimiter: one counter of videos opened at YouTube per minute for the whole instance,
// whoever opened them. The recommender takes only what the others left under the limit, and asks nothing for
// WallCooldown once YouTube refused a request, so it never makes that wall last longer for people watching.
type Limiter struct {
	cache        Cache
	limit        func(ctx context.Context) int
	wallCooldown time.Duration
	now          func() time.Time
}

const wallKey = "recommender:stream-wall:v1"

func NewLimiter(cache Cache, limit func(ctx context.Context) int, wallCooldown time.Duration) *Limiter {
	return &Limiter{cache: cache, limit: limit, wallCooldown: wallCooldown, now: time.Now}
}

// TryAcquire counts one request for the recommender and says whether it stays under the limit.
func (l *Limiter) TryAcquire(ctx context.Context) bool {
	limit := l.limit(ctx)
	if limit <= 0 || l.Walled(ctx) {
		return false
	}
	used, ok := l.count(ctx)
	return ok && used <= int64(limit)
}

// Opened records a video opened at YouTube; blocked raises the wall.
func (l *Limiter) Opened(ctx context.Context, alreadyCounted, blocked bool) {
	if !alreadyCounted {
		l.count(ctx)
	}
	if blocked {
		l.cache.Set(ctx, wallKey, strconv.FormatInt(l.now().UnixMilli(), 10), l.wallCooldown)
	}
}

func (l *Limiter) UsedThisMinute(ctx context.Context) int64 {
	raw, ok, _ := l.cache.Get(ctx, l.windowKey())
	if !ok {
		return 0
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

func (l *Limiter) Walled(ctx context.Context) bool {
	_, ok, _ := l.cache.Get(ctx, wallKey)
	return ok
}

func (l *Limiter) count(ctx context.Context) (int64, bool) {
	n, err := l.cache.Increment(ctx, l.windowKey(), 2*time.Minute)
	return n, err == nil
}

func (l *Limiter) windowKey() string {
	return "recommender:stream-requests:v1:" + strconv.FormatInt(l.now().UnixMilli()/60_000, 10)
}
