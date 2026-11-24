package db

import (
	"errors"
	"sync/atomic"
	"time"
)

var ErrDatabaseDown = errors.New("database unreachable")

// breaker stops new connections from each waiting the full connect timeout while the database is down: after one
// attempt failed, others fail at once and a single probe goes through every probeEvery.
type breaker struct {
	timeout, probeEvery time.Duration
	lastTry, lastOK     atomic.Int64
}

func (b *breaker) before(now time.Time) error {
	n := now.UnixNano()
	t := b.lastTry.Load()
	failed := t > b.lastOK.Load() && n-t > int64(b.timeout)
	if failed && n-t < int64(b.timeout+b.probeEvery) {
		return ErrDatabaseDown
	}
	if !b.lastTry.CompareAndSwap(t, n) && failed {
		return ErrDatabaseDown
	}
	return nil
}

func (b *breaker) after(now time.Time) { b.lastOK.Store(now.UnixNano()) }
