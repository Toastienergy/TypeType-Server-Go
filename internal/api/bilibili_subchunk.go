package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const subchunkSize = 1_000_000

// subchunkPrefetches is the four-slot pool of BilibiliSubchunkInputStream's prefetch executor.
var subchunkPrefetches = make(chan struct{}, 4)

func subchunksEnabled() bool {
	return envTrue("BILIBILI_SUBCHUNK_PROXY") || envTrue("BILIBILI_CONCURRENT_PREFETCH")
}

// splitRangeHeader is BilibiliRangeSplitter.parseRange: start and end of a single "bytes=" range, -1 when absent.
func splitRangeHeader(h string) (int64, int64, bool) {
	t := strings.TrimSpace(h)
	if len(t) < 6 || !strings.EqualFold(t[:6], "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimSpace(t[6:])
	if strings.Contains(spec, ",") {
		return 0, 0, false
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, false
	}
	start, end := int64(-1), int64(-1)
	if a = strings.TrimSpace(a); a != "" {
		n, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		start = n
	}
	if b = strings.TrimSpace(b); b != "" {
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		end = n
	}
	return start, end, start >= 0 || end >= 0
}

// readBilibiliSubchunked is readBilibiliSubchunkedRange: the first 1 MB on the first mirror that answers, then the
// rest of the range in 1 MB requests, each one resumable.
func (s *Server) readBilibiliSubchunked(ctx context.Context, mirrors []string, start, end int64) (*mediaReply, *commentErr) {
	firstEnd := start + subchunkSize - 1
	if end >= 0 {
		firstEnd = min(end, firstEnd)
	}
	first := "bytes=" + strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(firstEnd, 10)
	last := "Proxy fetch failed"
	for attempt := 1; attempt <= bilibiliRangeAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, &commentErr{499, "", ""}
		}
		active := mirrors[(attempt-1)%len(mirrors)]
		began := time.Now()
		res, err := s.proxyClient.Do(bilibiliRequest(ctx, active, first))
		if err != nil {
			mirrorTracker.failure(active)
			last = err.Error()
			continue
		}
		if !successful(res) {
			mirrorTracker.failure(active)
			last = "Upstream returned " + strconv.Itoa(res.StatusCode)
			res.Body.Close()
			continue
		}
		opened := time.Since(began)
		mirrorTracker.success(active, opened.Milliseconds())
		var others []string
		for _, m := range mirrors {
			if m != active {
				others = append(others, m)
			}
		}
		ct := res.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		if res.StatusCode != http.StatusPartialContent {
			body := newRetryingBody(s, ctx, active, first, res, bilibiliRangeAttempts-attempt, others, opened)
			length := ""
			if res.ContentLength >= 0 {
				length = strconv.FormatInt(res.ContentLength, 10)
			}
			return &mediaReply{status: res.StatusCode, ctype: ct, length: length, crange: res.Header.Get("Content-Range"),
				accept: res.Header.Get("Accept-Ranges"), body: body}, nil
		}
		total := contentTotal(res.Header.Get("Content-Range"))
		effEnd := firstEnd
		switch {
		case total > start:
			effEnd = total - 1
			if end >= 0 {
				effEnd = min(end, total-1)
			}
		case end >= 0:
			effEnd = end
		}
		var ranges [][2]int64
		for c := start; c <= effEnd; c += subchunkSize {
			ranges = append(ranges, [2]int64{c, min(c+subchunkSize-1, effEnd)})
		}
		totalPart := "*"
		if total > 0 {
			totalPart = strconv.FormatInt(total, 10)
		}
		accept := res.Header.Get("Accept-Ranges")
		if accept == "" {
			accept = "bytes"
		}
		sc := &subchunkBody{s: s, ctx: ctx, base: active, others: others, ranges: ranges, prefetched: -1}
		sc.cur = newRetryingBody(s, ctx, active, rangeHeader(ranges[0]), res, bilibiliRangeAttempts-1, others, opened)
		if envTrue("BILIBILI_CONCURRENT_PREFETCH") && len(ranges) > 1 {
			sc.prefetch(1)
		}
		return &mediaReply{status: 206, ctype: ct, length: strconv.FormatInt(effEnd-start+1, 10),
			crange: "bytes " + strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(effEnd, 10) + "/" + totalPart, accept: accept, body: sc}, nil
	}
	return nil, &commentErr{http.StatusUnprocessableEntity, last, "error"}
}

func rangeHeader(r [2]int64) string {
	return "bytes=" + strconv.FormatInt(r[0], 10) + "-" + strconv.FormatInt(r[1], 10)
}

// subchunkBody is BilibiliSubchunkInputStream.
type subchunkBody struct {
	s          *Server
	ctx        context.Context
	base       string
	others     []string
	ranges     [][2]int64
	index      int
	sent       int64
	cur        io.ReadCloser
	mu         sync.Mutex
	closed     bool
	prefetched int
	ahead      chan []byte
	cancel     context.CancelFunc
}

func (b *subchunkBody) Read(p []byte) (int, error) {
	for {
		if b.closed {
			return 0, errors.New("stream closed")
		}
		if b.cur == nil {
			return 0, io.EOF
		}
		n, err := b.cur.Read(p)
		if n > 0 {
			b.sent += int64(n)
			return n, nil
		}
		if err == io.EOF {
			b.cur.Close()
			b.cur = nil
			b.index++
			b.sent = 0
			if b.index >= len(b.ranges) {
				return 0, io.EOF
			}
			next, oerr := b.obtain(b.index)
			if oerr != nil {
				if b.ctx.Err() != nil {
					return 0, oerr
				}
				if rerr := b.recover(); rerr != nil {
					return 0, rerr
				}
				continue
			}
			b.cur = next
			if envTrue("BILIBILI_CONCURRENT_PREFETCH") {
				b.prefetch(b.index + 1)
			}
			continue
		}
		if err == nil {
			continue
		}
		if b.ctx.Err() != nil {
			return 0, err
		}
		if rerr := b.recover(); rerr != nil {
			return 0, rerr
		}
	}
}

// recover reopens what is left of the current subchunk, two attempts, as recoverCurrentSubchunk.
func (b *subchunkBody) recover() error {
	if b.cur != nil {
		b.cur.Close()
		b.cur = nil
	}
	full := b.ranges[b.index]
	from := full[0] + b.sent
	if from > full[1] {
		b.index++
		b.sent = 0
		if b.index < len(b.ranges) {
			next, err := b.obtain(b.index)
			if err != nil {
				return err
			}
			b.cur = next
		}
		return nil
	}
	b.ranges[b.index] = [2]int64{from, full[1]}
	b.sent = 0
	next, err := b.open([2]int64{from, full[1]}, 2)
	if err != nil {
		return err
	}
	b.cur = next
	return nil
}

func (b *subchunkBody) obtain(i int) (io.ReadCloser, error) {
	b.mu.Lock()
	ahead, at := b.ahead, b.prefetched
	b.ahead, b.prefetched = nil, -1
	b.mu.Unlock()
	if ahead != nil && at == i {
		if data, ok := <-ahead; ok && data != nil {
			return io.NopCloser(bytes.NewReader(data)), nil
		}
	} else if ahead != nil && b.cancel != nil {
		b.cancel()
	}
	return b.open(b.ranges[i], bilibiliRangeAttempts)
}

func (b *subchunkBody) open(r [2]int64, attempts int) (io.ReadCloser, error) {
	var mirrors []string
	for _, m := range append([]string{b.base}, b.others...) {
		if !slices.Contains(mirrors, m) {
			mirrors = append(mirrors, m)
		}
	}
	mirrors = mirrorTracker.prioritize(mirrors, bilibiliRegion())
	var last error = errors.New("failed to open subchunk")
	for attempt := 1; attempt <= attempts; attempt++ {
		if b.closed || b.ctx.Err() != nil {
			return nil, last
		}
		u := mirrors[(attempt-1)%len(mirrors)]
		began := time.Now()
		res, err := b.s.proxyClient.Do(bilibiliRequest(b.ctx, u, rangeHeader(r)))
		if err != nil {
			mirrorTracker.failure(u)
			last = err
			continue
		}
		if !successful(res) {
			res.Body.Close()
			mirrorTracker.failure(u)
			last = errors.New("upstream returned " + strconv.Itoa(res.StatusCode) + " for subchunk " + rangeHeader(r))
			continue
		}
		var rest []string
		for _, m := range mirrors {
			if m != u {
				rest = append(rest, m)
			}
		}
		return newRetryingBody(b.s, b.ctx, u, rangeHeader(r), res, attempts-attempt, rest, time.Since(began)), nil
	}
	return nil, last
}

// prefetch reads subchunk i whole into memory in the background, one of four slots across the server.
func (b *subchunkBody) prefetch(i int) {
	if i >= len(b.ranges) || b.closed {
		return
	}
	select {
	case subchunkPrefetches <- struct{}{}:
	default:
		return
	}
	ctx, cancel := context.WithCancel(b.ctx)
	ch := make(chan []byte, 1)
	b.mu.Lock()
	b.ahead, b.prefetched, b.cancel = ch, i, cancel
	b.mu.Unlock()
	go func() {
		defer func() { <-subchunkPrefetches }()
		sub := &subchunkBody{s: b.s, ctx: ctx, base: b.base, others: b.others}
		r, err := sub.open(b.ranges[i], bilibiliRangeAttempts)
		if err != nil {
			ch <- nil
			return
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		if err != nil {
			data = nil
		}
		ch <- data
	}()
}

func (b *subchunkBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	if b.cancel != nil {
		b.cancel()
	}
	if b.cur != nil {
		return b.cur.Close()
	}
	return nil
}
