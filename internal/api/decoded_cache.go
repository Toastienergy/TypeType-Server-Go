package api

import (
	"encoding/json"
	"sync"
)

// decodedValues keeps, for each Dragonfly key, the value last decoded from it next to the raw text it came from.
// Some answers read a few hundred KB of JSON from Dragonfly on every request and decoding it was most of the work;
// Dragonfly stays the source, and a changed text is decoded again. Values are shared, so callers only read them.
type decodedValues[T any] struct {
	mu sync.Mutex
	m  map[string]decodedValue[T]
}

type decodedValue[T any] struct {
	raw string
	v   T
}

const decodedKeys = 64

func (d *decodedValues[T]) decode(key, raw string) (T, bool) {
	d.mu.Lock()
	e, ok := d.m[key]
	d.mu.Unlock()
	if ok && e.raw == raw {
		return e.v, true
	}
	var v T
	if json.Unmarshal([]byte(raw), &v) != nil {
		return v, false
	}
	d.mu.Lock()
	if d.m == nil || len(d.m) >= decodedKeys {
		d.m = map[string]decodedValue[T]{}
	}
	d.m[key] = decodedValue[T]{raw, v}
	d.mu.Unlock()
	return v, true
}
