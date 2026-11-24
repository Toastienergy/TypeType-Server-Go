package cache

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/rueidis"
)

// Cache is the Kotlin CacheService on Dragonfly. rueidis pipelines concurrent commands on one connection, which
// is what keeps a cache hit in the tens of microseconds under load.
type Cache struct {
	c rueidis.Client
}

var (
	refreshScript   = rueidis.NewLuaScript("if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('expire', KEYS[1], ARGV[2]) end return 0")
	incrementScript = rueidis.NewLuaScript("local n = redis.call('incr', KEYS[1]) if n == 1 then redis.call('expire', KEYS[1], ARGV[1]) end return n")
)

func Open(url string) (*Cache, error) {
	opt, err := rueidis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	opt.DisableCache = true
	opt.DisableRetry = false
	c, err := rueidis.NewClient(opt)
	if err != nil {
		return nil, err
	}
	return &Cache{c: c}, nil
}

func (k *Cache) Close() { k.c.Close() }

// opTimeout bounds every cache call: a Dragonfly that stops answering must look like a cache miss within a moment,
// not hold the request until the client gives up. Writes run past a cancelled request so they still land.
const opTimeout = 300 * time.Millisecond

func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
}

// Get returns ok=false when the key is absent; any other failure is an error the caller may ignore.
func (k *Cache) Get(ctx context.Context, key string) (string, bool, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	v, err := k.c.Do(ctx, k.c.B().Get().Key(key).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (k *Cache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	return k.c.Do(ctx, k.c.B().Setex().Key(key).Seconds(seconds(ttl)).Value(value).Build()).Error()
}

func (k *Cache) SetIfAbsent(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	err := k.c.Do(ctx, k.c.B().Set().Key(key).Value(value).Nx().ExSeconds(seconds(ttl)).Build()).Error()
	if rueidis.IsRedisNil(err) {
		return false, nil
	}
	return err == nil, err
}

func (k *Cache) RefreshIfValueMatches(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	n, err := refreshScript.Exec(ctx, k.c, []string{key}, []string{value, strconv.FormatInt(seconds(ttl), 10)}).AsInt64()
	return n == 1, err
}

func (k *Cache) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	return incrementScript.Exec(ctx, k.c, []string{key}, []string{strconv.FormatInt(seconds(ttl), 10)}).AsInt64()
}

func (k *Cache) GetAndDelete(ctx context.Context, key string) (string, bool, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	v, err := k.c.Do(ctx, k.c.B().Getdel().Key(key).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (k *Cache) Delete(ctx context.Context, key string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	return k.c.Do(ctx, k.c.B().Del().Key(key).Build()).Error()
}

func (k *Cache) DeleteMatching(ctx context.Context, pattern string) (int, error) {
	deleted := 0
	var cursor uint64
	for {
		e, err := k.c.Do(ctx, k.c.B().Scan().Cursor(cursor).Match(pattern).Count(200).Build()).AsScanEntry()
		if err != nil {
			return deleted, err
		}
		if len(e.Elements) > 0 {
			n, err := k.c.Do(ctx, k.c.B().Del().Key(e.Elements...).Build()).AsInt64()
			if err != nil {
				return deleted, err
			}
			deleted += int(n)
		}
		if e.Cursor == 0 {
			return deleted, nil
		}
		cursor = e.Cursor
	}
}

func (k *Cache) Ping(ctx context.Context) bool {
	ctx, cancel := bounded(ctx)
	defer cancel()
	s, err := k.c.Do(ctx, k.c.B().Ping().Build()).ToString()
	return err == nil && s == "PONG"
}

func seconds(d time.Duration) int64 {
	s := int64(d / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}
