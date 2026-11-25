package httpx

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Metrics is AppMetrics: request totals, mean duration, and counts per status and per route, capped at 512 route
// keys so unusual paths cannot grow it without bound.
type Metrics struct {
	total, durationMs atomic.Int64
	mu                sync.Mutex
	status            map[int]int64
	routes            map[routeKey]int64
}

// routeKey is a struct so recording a request builds no string.
type routeKey struct {
	route  string
	status int
}

var Global = &Metrics{status: map[int]int64{}, routes: map[routeKey]int64{}}

func (m *Metrics) record(route string, status int, ms int64) {
	m.total.Add(1)
	m.durationMs.Add(ms)
	key := routeKey{route, status}
	m.mu.Lock()
	m.status[status]++
	if _, ok := m.routes[key]; ok || len(m.routes) < 512 {
		m.routes[key]++
	} else {
		m.routes[routeKey{"/__other__", status}]++
	}
	m.mu.Unlock()
}

func (m *Metrics) Snapshot() string {
	var b strings.Builder
	total := m.total.Load()
	avg := int64(0)
	if total > 0 {
		avg = m.durationMs.Load() / total
	}
	fmt.Fprintf(&b, "requests.total=%d\nrequests.durationMs.avg=%d\n", total, avg)
	m.mu.Lock()
	defer m.mu.Unlock()
	codes := make([]int, 0, len(m.status))
	for c := range m.status {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	for _, c := range codes {
		fmt.Fprintf(&b, "requests.status.%d=%d\n", c, m.status[c])
	}
	keys := make([]routeKey, 0, len(m.routes))
	for k := range m.routes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return strconv.Itoa(keys[i].status) < strconv.Itoa(keys[j].status)
	})
	for _, k := range keys {
		name := strings.ReplaceAll(strings.Trim(k.route, "/"), "/", ".")
		if name == "" {
			name = "root"
		}
		fmt.Fprintf(&b, "requests.route.%s.status.%d=%d\n", name, k.status, m.routes[k])
	}
	return b.String()
}
