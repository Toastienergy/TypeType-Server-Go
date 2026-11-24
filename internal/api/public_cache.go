package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

// extractFail is an extraction that did not succeed: a failure (provider down, timeout) the cache may cover with
// the last good answer, or a bad request answered as is.
type extractFail struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Code    string `json:"code"`
	bad     bool
}

type admitKind int

const (
	admitValid admitKind = iota
	admitSuspect
	admitReject
)

// admission is CacheAdmission: how long a fresh answer is kept, or that it looks wrong.
type admission struct {
	kind   admitKind
	ttl    time.Duration
	reason string
}

func valid(seconds int64) admission {
	return admission{kind: admitValid, ttl: time.Duration(seconds) * time.Second}
}

// suspect is CacheAdmission.Suspect: an empty first page is kept 30 s, or the last good answer is served instead.
var suspect = admission{kind: admitSuspect, ttl: 30 * time.Second}

func firstPageAdmission(next string, empty bool, seconds int64) admission {
	if next == "" && empty {
		return suspect
	}
	return valid(seconds)
}

// cacheArea is PublicCacheArea.
type cacheArea[T any] struct {
	name         string
	keepLastGood bool
	noBackoff    bool
	admit        func(T) admission
}

const (
	lastGoodTTL      = 24 * time.Hour
	backoffRetention = time.Hour
)

type cacheBackoff struct {
	Failures int    `json:"failures"`
	RetryAt  int64  `json:"retryAt"`
	Message  string `json:"message,omitempty"`
	Code     string `json:"code,omitempty"`
	Status   int    `json:"status,omitempty"`
}

type goodEnvelope[T any] struct {
	Value    T     `json:"value"`
	CachedAt int64 `json:"cachedAt"`
}

// publicCacheKey is PublicCacheKey.of: the area, then the first 32 hex digits of the SHA-256 of the parts.
func publicCacheKey(area string, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return area + ":v2:" + hex.EncodeToString(sum[:])[:32]
}

var publicFlights singleflight.Group

type flight[T any] struct {
	v T
	f *extractFail
}

// publicCached is PublicExtractionCache.getOrLoad: a cached answer, else one load per key across concurrent
// callers, its answer kept per the area's admission; a failure starts a backoff (30 s doubling up to 10 min) during
// which the last good answer, or the remembered failure, is served without asking the provider again.
func publicCached[T any](s *Server, ctx context.Context, area cacheArea[T], key string, load func(context.Context) (T, *extractFail)) (T, *extractFail) {
	if v, ok := readCached[T](s, ctx, key); ok {
		return v, nil
	}
	res, _, _ := publicFlights.Do(key, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		v, f := loadThrough(s, lctx, area, key, load)
		return flight[T]{v, f}, nil
	})
	r := res.(flight[T])
	return r.v, r.f
}

func loadThrough[T any](s *Server, ctx context.Context, area cacheArea[T], key string, load func(context.Context) (T, *extractFail)) (T, *extractFail) {
	if v, ok := readCached[T](s, ctx, key); ok {
		return v, nil
	}
	var backoff *cacheBackoff
	if !area.noBackoff {
		var b cacheBackoff
		if readJSON(s, ctx, key+":backoff", &b) {
			backoff = &b
		}
	}
	if backoff != nil && time.Now().UnixMilli() < backoff.RetryAt {
		if v, ok := lastGood[T](s, ctx, area, key); ok {
			return v, nil
		}
		if backoff.Message != "" {
			var zero T
			return zero, &extractFail{Status: backoff.Status, Message: backoff.Message, Code: backoff.Code}
		}
	}
	v, f := load(ctx)
	if f != nil {
		if f.bad {
			return v, f
		}
		return fallBack(s, ctx, area, key, backoff, f)
	}
	switch adm := area.admit(v); adm.kind {
	case admitValid:
		writeJSON(s, ctx, key, v, adm.ttl)
		if adm.ttl > 0 && area.keepLastGood {
			writeJSON(s, ctx, key+":good", goodEnvelope[T]{v, time.Now().UnixMilli()}, lastGoodTTL)
		}
		if backoff != nil {
			s.Cache.Delete(ctx, key+":backoff")
		}
	case admitSuspect:
		if good, ok := lastGood[T](s, ctx, area, key); ok {
			startBackoff(s, ctx, area, key, backoff, nil)
			return good, nil
		}
		writeJSON(s, ctx, key, v, adm.ttl)
	case admitReject:
		return fallBack(s, ctx, area, key, backoff, &extractFail{Status: 422, Message: adm.reason, Code: "cache_rejected"})
	}
	return v, nil
}

func fallBack[T any](s *Server, ctx context.Context, area cacheArea[T], key string, backoff *cacheBackoff, f *extractFail) (T, *extractFail) {
	startBackoff(s, ctx, area, key, backoff, f)
	if v, ok := lastGood[T](s, ctx, area, key); ok {
		return v, nil
	}
	var zero T
	return zero, f
}

func startBackoff[T any](s *Server, ctx context.Context, area cacheArea[T], key string, prev *cacheBackoff, f *extractFail) {
	if area.noBackoff {
		return
	}
	n := 1
	if prev != nil {
		n = prev.Failures + 1
	}
	delay := min(int64(30)<<min(n-1, 5), 600)
	next := cacheBackoff{Failures: n, RetryAt: time.Now().UnixMilli() + delay*1000}
	if f != nil {
		next.Message, next.Code, next.Status = f.Message, f.Code, f.Status
	}
	writeJSON(s, ctx, key+":backoff", next, backoffRetention)
	s.Log.Info("cache backoff", "area", area.name, "failures", n, "retryInSeconds", delay)
}

func lastGood[T any](s *Server, ctx context.Context, area cacheArea[T], key string) (T, bool) {
	var env goodEnvelope[T]
	if !area.keepLastGood || !readJSON(s, ctx, key+":good", &env) {
		var zero T
		return zero, false
	}
	return env.Value, true
}

func readCached[T any](s *Server, ctx context.Context, key string) (T, bool) {
	var v T
	return v, readJSON(s, ctx, key, &v)
}

func readJSON(s *Server, ctx context.Context, key string, into any) bool {
	raw, ok, err := s.Cache.Get(ctx, key)
	return err == nil && ok && json.Unmarshal([]byte(raw), into) == nil
}

func writeJSON(s *Server, ctx context.Context, key string, v any, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	if b, err := json.Marshal(v); err == nil {
		s.Cache.Set(ctx, key, string(b), ttl)
	}
}
