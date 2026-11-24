package httpx

import (
	"fmt"
	"sort"
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
	routes            map[string]int64
}

var Global = &Metrics{status: map[int]int64{}, routes: map[string]int64{}}

func (m *Metrics) Record(path string, status int, ms int64) {
	m.total.Add(1)
	m.durationMs.Add(ms)
	key := MetricPath(path) + "|" + fmt.Sprint(status)
	m.mu.Lock()
	m.status[status]++
	if _, ok := m.routes[key]; ok || len(m.routes) < 512 {
		m.routes[key]++
	} else {
		m.routes["/__other__|"+fmt.Sprint(status)]++
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
	keys := make([]string, 0, len(m.routes))
	for k := range m.routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		route, status, _ := strings.Cut(k, "|")
		name := strings.ReplaceAll(strings.Trim(route, "/"), "/", ".")
		if name == "" {
			name = "root"
		}
		fmt.Fprintf(&b, "requests.route.%s.status.%s=%d\n", name, status, m.routes[k])
	}
	return b.String()
}
