package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	warmParallel     = 16
	warmDashHeader   = 64 << 10
	warmPlaylistMax  = 512 << 10
	warmSegmentMax   = 4 << 20
	warmTimeout      = 20 * time.Second
	startHedgeAfter  = 1200 * time.Millisecond
	startPendingWait = 4 * time.Second
)

// warmMedia is one stream of a response, as ProviderMediaWarmup.Media.
type warmMedia struct {
	url                  string
	indexStart, indexEnd int64
	starts               bool
	bitrate              int64
	video                bool
}

// warmup is ProviderMediaWarmup with its segment table and HLS chain: it fetches the start of the tracks a player
// opens first while the client still reads the stream response, and the next segment while it plays one.
type warmup struct {
	s       *Server
	cache   *startCache
	permits chan struct{}
	mu      sync.Mutex
	tables  map[string][][2]int64
	order   []string
	chain   *hlsChain
}

func newWarmup(s *Server) *warmup {
	return &warmup{s: s, cache: newStartCache(), permits: make(chan struct{}, warmParallel), tables: map[string][][2]int64{}, chain: newHLSChain()}
}

func (w *warmup) acquire(ctx context.Context) bool {
	select {
	case w.permits <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (w *warmup) release() { <-w.permits }

func (w *warmup) available() bool { return len(w.permits) < cap(w.permits) }

// remember is ProviderMediaSegmentTable.remember, 512 files at most.
func (w *warmup) remember(url string, segs [][2]int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.tables[url]; !ok {
		w.order = append(w.order, url)
	}
	w.tables[url] = segs
	for len(w.order) > 512 {
		delete(w.tables, w.order[0])
		w.order = w.order[1:]
	}
}

func (w *warmup) after(url string, start int64) ([2]int64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	segs := w.tables[url]
	i := sort.Search(len(segs), func(i int) bool { return segs[i][0] >= start })
	if i < len(segs) && segs[i][0] == start && i+1 < len(segs) {
		return segs[i+1], true
	}
	return [2]int64{}, false
}

// startURLs is ProviderMediaStartTracks.urls: the tracks the web player opens first.
func startURLs(provider string, video []videoStream, audio []audioStream) map[string]bool {
	out := map[string]bool{}
	if provider == "niconico" {
		for _, v := range video {
			out[v.URL] = true
		}
		for _, a := range audio {
			out[a.URL] = true
		}
		return out
	}
	rank := func(c *string) int {
		switch {
		case c == nil:
			return 3
		case strings.HasPrefix(*c, "avc1"):
			return 0
		case strings.HasPrefix(*c, "av01"):
			return 1
		case strings.HasPrefix(*c, "hev1"), strings.HasPrefix(*c, "hvc1"):
			return 2
		}
		return 3
	}
	bits := func(b *int64) int64 {
		if b == nil {
			return 0
		}
		return *b
	}
	best := 4
	for _, v := range video {
		best = min(best, rank(v.Codec))
	}
	var start *videoStream
	var lowest *videoStream
	for i := range video {
		v := &video[i]
		if rank(v.Codec) != best {
			continue
		}
		if b := bits(v.Bitrate); b >= 1 && b <= 1_000_000 && (start == nil || b > bits(start.Bitrate)) {
			start = v
		}
		if lowest == nil || bitsOr(v.Bitrate) < bitsOr(lowest.Bitrate) {
			lowest = v
		}
	}
	if start == nil {
		start = lowest
	}
	if start != nil {
		out[start.URL] = true
	}
	var top *audioStream
	for i := range audio {
		if top == nil || bits(audio[i].Bitrate) > bits(top.Bitrate) {
			top = &audio[i]
		}
	}
	if top != nil {
		out[top.URL] = true
	}
	return out
}

func bitsOr(b *int64) int64 {
	if b == nil {
		return 1<<31 - 1
	}
	return *b
}

// warmStreams is ProviderMediaWarmup.warm, run in the background once a stream response is written.
func (s *Server) warmStreams(provider string, video []videoStream, audio []audioStream) {
	w := s.warm
	if w == nil {
		return
	}
	starting := startURLs(provider, video, audio)
	var media []warmMedia
	for _, v := range video {
		media = append(media, warmMedia{v.URL, v.IndexStart, v.IndexEnd, starting[v.URL], bitsOrZero(v.Bitrate), true})
	}
	for _, a := range audio {
		media = append(media, warmMedia{a.URL, a.IndexStart, a.IndexEnd, starting[a.URL], bitsOrZero(a.Bitrate), false})
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
		defer cancel()
		if provider == "bilibili" {
			var wg sync.WaitGroup
			for _, m := range media {
				wg.Go(func() {
					if t := w.targetOf(ctx, m.url); t != nil {
						w.warmDash(ctx, t, m)
					}
				})
			}
			wg.Wait()
			return
		}
		w.warmHLS(ctx, media, len(video))
	}()
}

func bitsOrZero(b *int64) int64 {
	if b == nil {
		return 0
	}
	return *b
}

var handleInPath = regexp.MustCompile(`m1_[A-Za-z0-9_-]{24}`)

// targetOf resolves a /media handle (or a raw provider URL) to its upstream target.
func (w *warmup) targetOf(ctx context.Context, ref string) *mediaTarget {
	p, _, _ := strings.Cut(ref, "?")
	h := p[strings.LastIndex(p, "/")+1:]
	if !mediaHandleRe.MatchString(h) {
		return &mediaTarget{URL: ref}
	}
	v, ok, err := w.s.Cache.Get(ctx, "provider-media:v1:"+h)
	var t mediaTarget
	if err != nil || !ok || json.Unmarshal([]byte(v), &t) != nil {
		return nil
	}
	return &t
}

// fetch is ProviderMediaWarmup.fetch: the answer goes to the cache under key when it is at most limit bytes.
func (w *warmup) fetch(ctx context.Context, t *mediaTarget, rng string, limit int, key string, offset int64) []byte {
	m, cerr := w.s.openMedia(ctx, append([]string{t.URL}, t.Alternates...), t.DomandBid, rng)
	if cerr != nil {
		return nil
	}
	defer m.close()
	if m.status != 200 && m.status != 206 {
		return nil
	}
	data := m.text
	if data == nil {
		b, err := io.ReadAll(io.LimitReader(m.body, int64(limit)+1))
		if err != nil || len(b) > limit {
			return nil
		}
		data = b
	} else if len(data) > limit {
		return nil
	}
	total := contentTotal(m.crange)
	if total == 0 && rng == "" {
		total, _ = strconv.ParseInt(m.length, 10, 64)
	}
	complete := offset == 0 && (rng == "" || total > 0 && int64(len(data)) == total)
	w.cache.put(key, &startEntry{bytes: data, total: total, ctype: m.ctype, complete: complete, offset: offset})
	return data
}

func contentTotal(crange string) int64 {
	if i := strings.LastIndexByte(crange, '/'); i >= 0 {
		n, _ := strconv.ParseInt(crange[i+1:], 10, 64)
		return n
	}
	return 0
}

func (w *warmup) fetchClaimed(ctx context.Context, t *mediaTarget, rng string, limit int, claim chan struct{}, key string, offset int64) []byte {
	defer w.cache.finish(key, claim)
	if !w.acquire(ctx) {
		return nil
	}
	defer w.release()
	return w.fetch(ctx, t, rng, limit, key, offset)
}

// prefetch is ProviderMediaWarmup.prefetch: the cached bytes, the bytes of a fetch already running, or a new one.
func (w *warmup) prefetch(ctx context.Context, t *mediaTarget, rng string, limit int) []byte {
	if e := w.cache.get(t.URL); e != nil {
		return e.bytes
	}
	claim := w.cache.begin(t.URL, false)
	if claim == nil {
		w.cache.waitPending(t.URL, startPendingWait)
		if e := w.cache.get(t.URL); e != nil {
			return e.bytes
		}
		return nil
	}
	return w.fetchClaimed(ctx, t, rng, limit, claim, t.URL, 0)
}

// prefetchSegment is ProviderMediaWarmup.prefetchSegment: one segment ahead, skipped when every slot is busy.
func (w *warmup) prefetchSegment(t *mediaTarget, seg [2]int64) {
	if !w.available() {
		return
	}
	key := segmentKey(t.URL, seg[0])
	claim := w.cache.begin(key, false)
	if claim == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
		defer cancel()
		w.fetchClaimed(ctx, t, "bytes="+strconv.FormatInt(seg[0], 10)+"-"+strconv.FormatInt(seg[1], 10), warmSegmentMax, claim, key, seg[0])
	}()
}

// warmDash is ProviderMediaWarmup.warmDash: the header and segment index of every track, and the first segment of
// the start tracks.
func (w *warmup) warmDash(ctx context.Context, t *mediaTarget, m warmMedia) {
	if m.indexEnd <= 0 || m.indexEnd >= warmDashHeader {
		return
	}
	if m.starts && w.dashStart(ctx, t, m) {
		return
	}
	header := w.prefetch(ctx, t, "bytes=0-"+strconv.FormatInt(m.indexEnd, 10), warmDashHeader)
	if header == nil {
		return
	}
	segs := sidxSegments(header, m.indexStart)
	if segs == nil {
		return
	}
	w.remember(t.URL, segs)
	if !m.starts {
		return
	}
	end := segs[0][1]
	if end < int64(len(header)) || end >= warmSegmentMax {
		return
	}
	if claim := w.cache.begin(t.URL, true); claim != nil {
		w.fetchClaimed(ctx, t, "bytes=0-"+strconv.FormatInt(end, 10), warmSegmentMax, claim, t.URL, 0)
	}
}

// dashStart is DashStartFetch: one upstream request brings the header, stored as soon as it arrives, then the first
// segment. It answers false when another fetch owns the track or the first segment ends past the guess.
func (w *warmup) dashStart(ctx context.Context, t *mediaTarget, m warmMedia) bool {
	claim := w.cache.begin(t.URL, false)
	if claim == nil {
		return false
	}
	defer w.cache.finish(t.URL, claim)
	if !w.acquire(ctx) {
		return true
	}
	defer w.release()
	bps := m.bitrate
	if bps <= 0 {
		bps = 1_000_000
	}
	last := m.indexEnd + min(max(bps/8*10, 512<<10), 3<<20)
	r, cerr := w.s.openMedia(ctx, append([]string{t.URL}, t.Alternates...), t.DomandBid, "bytes=0-"+strconv.FormatInt(last, 10))
	if cerr != nil {
		return true
	}
	defer r.close()
	if r.status != 200 && r.status != 206 || r.body == nil {
		return true
	}
	total := contentTotal(r.crange)
	if total == 0 && r.status == 200 {
		total, _ = strconv.ParseInt(r.length, 10, 64)
	}
	header := make([]byte, m.indexEnd+1)
	if _, err := io.ReadFull(r.body, header); err != nil {
		return true
	}
	w.cache.put(t.URL, &startEntry{bytes: header, total: total, ctype: r.ctype, complete: total == int64(len(header))})
	segs := sidxSegments(header, m.indexStart)
	if segs != nil {
		w.remember(t.URL, segs)
	}
	if segs == nil || segs[0][1] < int64(len(header)) {
		return true
	}
	if segs[0][1] > last {
		return false
	}
	rest := make([]byte, segs[0][1]+1-int64(len(header)))
	if _, err := io.ReadFull(r.body, rest); err != nil {
		return true
	}
	full := append(header, rest...)
	w.cache.put(t.URL, &startEntry{bytes: full, total: total, ctype: r.ctype, complete: total == int64(len(full))})
	return true
}

// sidxSegments is Mp4SegmentIndex.segments: the byte ranges a sidx box at indexStart lists.
func sidxSegments(h []byte, indexStart int64) [][2]int64 {
	if indexStart < 0 || indexStart+8 > int64(len(h)) {
		return nil
	}
	b := h[indexStart:]
	size := int64(binary.BigEndian.Uint32(b))
	if string(b[4:8]) != "sidx" || size < 8 || indexStart+size > int64(len(h)) {
		return nil
	}
	p := 8
	need := func(n int) bool { return p+n <= len(b) }
	if !need(12) {
		return nil
	}
	version := b[p]
	p += 12
	var first int64
	if version == 0 {
		if !need(8) {
			return nil
		}
		first = int64(binary.BigEndian.Uint32(b[p+4:]))
		p += 8
	} else {
		if !need(16) {
			return nil
		}
		first = int64(binary.BigEndian.Uint64(b[p+8:]))
		p += 16
	}
	if !need(4) {
		return nil
	}
	count := int(binary.BigEndian.Uint16(b[p+2:]))
	p += 4
	if count == 0 || first < 0 || len(b)-p < count*12 {
		return nil
	}
	start := indexStart + size + first
	out := make([][2]int64, 0, count)
	for range count {
		ref := int32(binary.BigEndian.Uint32(b[p:]))
		if ref <= 0 {
			return nil
		}
		out = append(out, [2]int64{start, start + int64(ref) - 1})
		start += int64(ref)
		p += 12
	}
	return out
}
