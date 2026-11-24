package api

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeInterval  = 3 * time.Minute
	probeBytes     = 128 << 10
	probeMinBytes  = 64 << 10
	watchTick      = time.Minute
	watchActive    = 30 * time.Minute
	watchJitter    = 2 * time.Minute
	watchFetchGap  = 30 * time.Minute
	watchMinLeft   = 120
	watchSampleKey = "bilibili-mirror-sample:v1"
)

// mirrorProber is BilibiliMirrorProber: a 128 KiB read in the middle of the file on each mirror host, at most once
// every three minutes per host, feeding the speed tracker.
type mirrorProber struct {
	s    *Server
	mu   sync.Mutex
	last map[string]time.Time
}

func (p *mirrorProber) probe(urls []string, total int64) {
	seen := map[string]bool{}
	for _, u := range urls {
		h := hostOf(u)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		now := time.Now()
		p.mu.Lock()
		if mirrorTracker.fresh(h, probeInterval) {
			p.last[h] = now
			p.mu.Unlock()
			continue
		}
		prev, ok := p.last[h]
		claimed := !ok || now.Sub(prev) >= probeInterval
		if claimed {
			p.last[h] = now
		}
		p.mu.Unlock()
		if claimed {
			go p.measure(u, total)
		}
	}
}

// measure reads a range rarely in the CDN edge cache, so it times the real path.
func (p *mirrorProber) measure(u string, total int64) {
	start := int64(512 << 10)
	if total > probeBytes*8 {
		start = total/4 + rand.Int64N(total/2-total/4)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	began := time.Now()
	res, err := p.s.proxyClient.Do(bilibiliRequest(ctx, u, "bytes="+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(start+probeBytes-1, 10)))
	if err != nil {
		mirrorTracker.failure(u)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusPartialContent {
		if !successful(res) && res.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			mirrorTracker.failure(u)
		}
		return
	}
	opened := time.Now()
	mirrorTracker.success(u, opened.Sub(began).Milliseconds())
	n, err := io.Copy(io.Discard, res.Body)
	if err != nil {
		mirrorTracker.failure(u)
		return
	}
	if secs := time.Since(opened).Seconds(); n >= probeMinBytes && secs > 0 {
		mirrorTracker.throughput(u, float64(n)/secs)
	}
}

type mirrorSample struct {
	urls  []string
	total int64
}

var deadlineRe = regexp.MustCompile(`[?&]deadline=(\d+)`)

func (m *mirrorSample) secondsLeft(now time.Time) int64 {
	for _, u := range m.urls {
		if d := deadlineRe.FindStringSubmatch(u); d != nil {
			n, _ := strconv.ParseInt(d[1], 10, 64)
			return n - now.Unix()
		}
	}
	return 0
}

func (m *mirrorSample) valid(now time.Time) bool {
	return m != nil && len(m.urls) > 0 && m.secondsLeft(now) > watchMinLeft
}

// mirrorWatch is BilibiliMirrorWatch: while Bilibili is played (in the last 30 minutes) and the admin allows it,
// the mirrors of a recent file are probed every three to five minutes, so ranking stays fresh between plays.
type mirrorWatch struct {
	s                         *Server
	prober                    *mirrorProber
	mu                        sync.Mutex
	sample                    *mirrorSample
	lastUse, lastProbe, fetch time.Time
	next                      time.Duration
}

func (s *Server) startMirrorWatch() {
	p := &mirrorProber{s: s, last: map[string]time.Time{}}
	s.mirrorWatch = &mirrorWatch{s: s, prober: p, next: probeInterval}
	go func() {
		time.Sleep(20 * time.Second)
		for {
			if st, err := s.Admin.Get(context.Background()); err == nil && st.BilibiliMirrorProbingEnabled {
				s.mirrorWatch.tick()
			}
			time.Sleep(watchTick)
		}
	}()
}

// remember is BilibiliMirrorWatch.remember plus the prober call the warmup makes for a start video track.
func (w *mirrorWatch) remember(urls []string, total int64) {
	if w == nil {
		return
	}
	w.prober.probe(urls, total)
	now := time.Now()
	w.mu.Lock()
	w.lastUse = now
	fresh := &mirrorSample{urls, total}
	ok := fresh.valid(now)
	if ok {
		w.sample = fresh
	}
	w.mu.Unlock()
	if ok {
		ttl := time.Duration(fresh.secondsLeft(now)) * time.Second
		w.s.Cache.Set(context.Background(), watchSampleKey, strconv.FormatInt(total, 10)+"\n"+strings.Join(urls, "\n"), ttl)
	}
}

func (w *mirrorWatch) tick() {
	now := time.Now()
	w.mu.Lock()
	if w.lastUse.IsZero() || now.Sub(w.lastUse) >= watchActive || now.Sub(w.lastProbe) < w.next {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	sample := w.current(now)
	if sample == nil {
		return
	}
	w.mu.Lock()
	w.lastProbe = now
	w.next = probeInterval + time.Duration(rand.Int64N(int64(watchJitter)+1))
	w.mu.Unlock()
	w.prober.probe(sample.urls, sample.total)
}

func (w *mirrorWatch) current(now time.Time) *mirrorSample {
	w.mu.Lock()
	sm := w.sample
	w.mu.Unlock()
	if sm.valid(now) {
		return sm
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if v, ok, _ := w.s.Cache.Get(ctx, watchSampleKey); ok {
		lines := strings.Split(v, "\n")
		if len(lines) >= 2 {
			total, _ := strconv.ParseInt(lines[0], 10, 64)
			var urls []string
			for _, l := range lines[1:] {
				if strings.TrimSpace(l) != "" {
					urls = append(urls, l)
				}
			}
			if c := (&mirrorSample{urls, total}); c.valid(now) {
				w.mu.Lock()
				w.sample = c
				w.mu.Unlock()
				return c
			}
		}
	}
	w.mu.Lock()
	if now.Sub(w.fetch) < watchFetchGap {
		w.mu.Unlock()
		return nil
	}
	w.fetch = now
	w.mu.Unlock()
	c := w.fetchSample(ctx)
	if c.valid(now) {
		w.mu.Lock()
		w.sample = c
		w.mu.Unlock()
		return c
	}
	return nil
}

// fetchSample is BilibiliMirrorSampleSource: the lowest-bitrate video track of the first trending video.
func (w *mirrorWatch) fetchSample(ctx context.Context) *mirrorSample {
	items, err := w.s.bilibiliTrending(ctx)
	if err != nil {
		return nil
	}
	for _, it := range items {
		if !strings.Contains(it.URL, "bilibili.com/video/") {
			continue
		}
		v, err := w.s.bili.Video(ctx, it.URL)
		if err != nil || len(v.VideoOnly) == 0 {
			return nil
		}
		low := v.VideoOnly[0]
		for _, m := range v.VideoOnly[1:] {
			if m.Bandwidth < low.Bandwidth {
				low = m
			}
		}
		return &mirrorSample{urls: append([]string{low.URL}, uposMirrors(low.URL, akamaiBlacklisted(), bilibiliRegion())...)}
	}
	return nil
}
