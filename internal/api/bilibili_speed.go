package api

import (
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// mirrorStats is BilibiliMirrorSpeedTracker.HostStats.
type mirrorStats struct {
	latency                          float64
	samples, failures                int
	lastFailure, lastSample, lastLat time.Time
	bps                              float64
	lastBps                          time.Time
}

// mirrorSpeed is BilibiliMirrorSpeedTracker: per upos host, a smoothed open latency, a throughput and a growing
// penalty after failures, to rank the mirrors of a file by the time a segment is expected to take.
type mirrorSpeed struct {
	mu    sync.Mutex
	stats map[string]*mirrorStats
}

var mirrorTracker = &mirrorSpeed{stats: map[string]*mirrorStats{}}

const (
	mirrorPenalty      = 15_000.0
	mirrorPenaltyBase  = 120 * time.Second
	mirrorStaleSample  = 90 * time.Second
	mirrorThroughputTL = 30 * time.Minute
	typicalSegment     = 512.0 * 1024
	unknownLatency     = 600.0
	unknownBps         = 400.0 * 1024
)

var allUpos = []string{"upos-sz-mirroraliov.bilivideo.com", "upos-sz-mirrorcosov.bilivideo.com", "upos-sz-mirrorali.bilivideo.com",
	"upos-sz-mirrorhw.bilivideo.com", "upos-sz-mirrorcos.bilivideo.com", "upos-hz-mirrorakam.akamaized.net"}

func uposHostOf(urlOrHost string) string {
	h := urlOrHost
	if strings.Contains(urlOrHost, "://") {
		u, err := url.Parse(urlOrHost)
		if err != nil {
			return ""
		}
		h = u.Hostname()
	}
	h = strings.ToLower(h)
	if !uposHost.MatchString(h) {
		return ""
	}
	return h
}

func isOverseasUpos(h string) bool {
	h = strings.ToLower(h)
	return strings.Contains(h, "mirroraliov") || strings.Contains(h, "mirrorcosov") || strings.Contains(h, "oversea") || strings.Contains(h, "ov.")
}

func isDomesticUpos(h string) bool {
	return !isOverseasUpos(h) && !strings.HasSuffix(strings.ToLower(h), ".akamaized.net")
}

func (m *mirrorSpeed) entry(u string) *mirrorStats {
	h := uposHostOf(u)
	if h == "" {
		return nil
	}
	s := m.stats[h]
	if s == nil {
		s = &mirrorStats{}
		m.stats[h] = s
	}
	return s
}

func (m *mirrorSpeed) success(u string, latencyMs int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.entry(u)
	if s == nil {
		return
	}
	now := time.Now()
	s.failures, s.lastSample, s.lastLat = 0, now, now
	sample := math.Max(float64(latencyMs), 1)
	if s.samples == 0 {
		s.latency = sample
	} else {
		s.latency = s.latency*0.7 + sample*0.3
	}
	s.samples++
}

func (m *mirrorSpeed) throughput(u string, bps float64) {
	if bps <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.entry(u)
	if s == nil {
		return
	}
	now := time.Now()
	if now.Sub(s.lastBps) < mirrorThroughputTL && s.bps > 0 {
		s.bps = s.bps*0.5 + bps*0.5
	} else {
		s.bps = bps
	}
	s.lastBps = now
}

func (m *mirrorSpeed) failure(u string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.entry(u); s != nil {
		s.failures++
		s.lastFailure = time.Now()
	}
}

func penaltyDuration(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	return mirrorPenaltyBase << min(max(failures-1, 0), 4)
}

func (s *mirrorStats) penalty(now time.Time) float64 {
	if s.failures > 0 && now.Sub(s.lastFailure) < penaltyDuration(s.failures) {
		return mirrorPenalty * float64(s.failures)
	}
	return 0
}

// expected is expectedSegmentMillis: latency plus the time a typical segment takes, plus the failure penalty.
func (m *mirrorSpeed) expected(u string, now time.Time) float64 {
	h := uposHostOf(u)
	if h == "" {
		return math.MaxFloat64
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stats[h]
	if s == nil {
		return unknownLatency + typicalSegment*1000/unknownBps
	}
	lat := unknownLatency
	if s.samples > 0 && now.Sub(s.lastLat) < mirrorThroughputTL {
		lat = s.latency
	}
	rate := unknownBps
	if s.bps > 0 && now.Sub(s.lastBps) < mirrorThroughputTL {
		rate = s.bps
	}
	return lat + typicalSegment*1000/rate + s.penalty(now)
}

func (m *mirrorSpeed) rank(urls []string) []string {
	now := time.Now()
	out := slices.Clone(urls)
	score := map[string]float64{}
	for _, u := range out {
		score[u] = m.expected(u, now)
	}
	slices.SortStableFunc(out, func(a, b string) int {
		switch {
		case score[a] < score[b]:
			return -1
		case score[a] > score[b]:
			return 1
		}
		return 0
	})
	return out
}

func (m *mirrorSpeed) fresh(u string, maxAge time.Duration) bool {
	h := uposHostOf(u)
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stats[h]
	if s == nil {
		return false
	}
	now := time.Now()
	return s.bps > 0 && now.Sub(s.lastBps) < maxAge || s.samples > 0 && now.Sub(s.lastLat) < maxAge
}

// score and prioritize order mirrors by region bucket, then measured latency, then the static mirror order.
func (m *mirrorSpeed) score(u, region string, now time.Time) float64 {
	h := uposHostOf(u)
	if h == "" {
		return math.MaxFloat64
	}
	offset := 0.0
	switch region {
	case "overseas":
		switch {
		case isOverseasUpos(h):
		case isDomesticUpos(h):
			offset = 10_000
		default:
			offset = 20_000
		}
	case "domestic":
		switch {
		case isDomesticUpos(h):
		case isOverseasUpos(h):
			offset = 10_000
		default:
			offset = 20_000
		}
	}
	static := 5_000.0
	if i := slices.Index(allUpos, h); i >= 0 {
		static = float64(i)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stats[h]
	if s == nil {
		return offset + static
	}
	if s.samples > 0 && now.Sub(s.lastSample) <= mirrorStaleSample {
		return offset + s.latency + s.penalty(now)
	}
	return offset + static + s.penalty(now)
}

func (m *mirrorSpeed) prioritize(urls []string, region string) []string {
	if region == "" {
		return urls
	}
	now := time.Now()
	out := slices.Clone(urls)
	slices.SortStableFunc(out, func(a, b string) int {
		sa, sb := m.score(a, region, now), m.score(b, region, now)
		switch {
		case sa < sb:
			return -1
		case sa > sb:
			return 1
		}
		return 0
	})
	return out
}

// bilibiliMirrorOrder is the mirror list readBilibiliRangeWithRetry tries: the measured best first when no CDN
// region is set, the region's mirrors first otherwise, Akamai left out when it is blacklisted.
func bilibiliMirrorOrder(primary string, alternates []string) []string {
	blocked, region := akamaiBlacklisted(), bilibiliRegion()
	primaryAkamai := isAkamaiURL(primary)
	if len(alternates) == 0 {
		alternates = uposMirrors(primary, blocked, region)
	}
	var distinct []string
	for _, a := range alternates {
		if !slices.Contains(distinct, a) {
			distinct = append(distinct, a)
		}
	}
	sorted := mirrorTracker.prioritize(distinct, region)
	host := ""
	if u, err := url.Parse(primary); err == nil {
		host = u.Hostname()
	}
	preferred := true
	switch region {
	case "overseas":
		preferred = isOverseasUpos(host)
	case "domestic":
		preferred = isDomesticUpos(host)
	}
	if blocked && primaryAkamai || !preferred && len(sorted) > 0 {
		var out []string
		for _, a := range sorted {
			if (!blocked || !isAkamaiURL(a)) && !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
		if !(blocked && primaryAkamai) && !slices.Contains(out, primary) {
			out = append(out, primary)
		}
		if len(out) == 0 {
			out = []string{primary}
		}
		return out
	}
	listed := []string{primary}
	for _, a := range sorted {
		if a != primary {
			listed = append(listed, a)
		}
	}
	if region == "" {
		listed = mirrorTracker.rank(listed)
	}
	if blocked {
		var kept []string
		for _, a := range listed {
			if !isAkamaiURL(a) {
				kept = append(kept, a)
			}
		}
		if len(kept) > 0 {
			return kept
		}
	}
	return listed
}
