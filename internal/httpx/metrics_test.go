package httpx

import "testing"

func TestMetricsSnapshot(t *testing.T) {
	m := &Metrics{status: map[int]int64{}, routes: map[routeKey]int64{}}
	m.record("/streams", 200, 10)
	m.record("/streams", 200, 30)
	m.record("/streams", 404, 2)
	m.record("/", 200, 0)
	want := "requests.total=4\nrequests.durationMs.avg=10\nrequests.status.200=3\nrequests.status.404=1\n" +
		"requests.route.root.status.200=1\nrequests.route.streams.status.200=2\nrequests.route.streams.status.404=1\n"
	if got := m.Snapshot(); got != want {
		t.Fatalf("snapshot:\n%s", got)
	}
}

func BenchmarkMetricsRecord(b *testing.B) {
	m := &Metrics{status: map[int]int64{}, routes: map[routeKey]int64{}}
	b.ReportAllocs()
	for b.Loop() {
		m.record("/sabr/playback/{sessionId}", 200, 1)
	}
}
