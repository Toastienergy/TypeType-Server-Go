package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const bilibiliRangeAttempts = 3

var (
	errChangedBody   = errors.New("upstream proxy body changed during retry")
	contentRangeRe   = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+|\*)$`)
	mirrorPermits    = make(chan struct{}, 8)
	mirrorClient     = newMirrorClient()
	errNoMirrorReply = errors.New("no Bilibili mirror answered")
)

// newMirrorClient is the mirror executor: 2 s to connect and 2 s between reads, as OkHttpProxyService's.
func newMirrorClient() *http.Client {
	d := &net.Dialer{Timeout: 2 * time.Second, Control: publicOnly}
	return &http.Client{Transport: &http.Transport{DialContext: d.DialContext, ResponseHeaderTimeout: 2 * time.Second, ForceAttemptHTTP2: true,
		MaxIdleConnsPerHost: 8}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type contentRange struct {
	start, end, total int64 // total -1 when "*"
}

func parseContentRange(v string) (contentRange, bool) {
	m := contentRangeRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return contentRange{}, false
	}
	s, _ := strconv.ParseInt(m[1], 10, 64)
	e, _ := strconv.ParseInt(m[2], 10, 64)
	t := int64(-1)
	if m[3] != "*" {
		t, _ = strconv.ParseInt(m[3], 10, 64)
	}
	return contentRange{s, e, t}, true
}

func bilibiliRequest(ctx context.Context, target, rng string) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
	req.Header.Set("User-Agent", bilibiliUA)
	req.Header.Set("Referer", "https://www.bilibili.com")
	req.Header.Set("Accept", "*/*")
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	return req
}

func successful(res *http.Response) bool { return res.StatusCode/100 == 2 }

// hedgedOpen is BilibiliHedgedOpen: the primary mirror, and after 350 ms without an answer a second one too; the
// first successful answer wins and the other is closed.
func (s *Server) hedgedOpen(ctx context.Context, primary, alternate, rng string) (string, *http.Response, error) {
	open := func(u string) (*http.Response, error) {
		res, err := s.proxyClient.Do(bilibiliRequest(ctx, u, rng))
		if err != nil {
			return nil, err
		}
		if !successful(res) {
			res.Body.Close()
			return nil, nil
		}
		return res, nil
	}
	if alternate == "" {
		res, err := s.proxyClient.Do(bilibiliRequest(ctx, primary, rng))
		return primary, res, err
	}
	type result struct {
		url string
		res *http.Response
		err error
	}
	results := make(chan result, 2)
	go func() { r, e := open(primary); results <- result{primary, r, e} }()
	timer := time.NewTimer(350 * time.Millisecond)
	defer timer.Stop()
	outstanding := 1
	select {
	case r := <-results:
		outstanding = 0
		if r.err != nil {
			res, err := s.proxyClient.Do(bilibiliRequest(ctx, alternate, rng))
			return alternate, res, err
		}
		if r.res != nil {
			return r.url, r.res, nil
		}
	case <-timer.C:
	}
	go func() { r, e := open(alternate); results <- result{alternate, r, e} }()
	outstanding++
	for outstanding > 0 {
		r := <-results
		outstanding--
		if r.res == nil {
			continue
		}
		if outstanding > 0 {
			go func() {
				if late := <-results; late.res != nil {
					late.res.Body.Close()
				}
			}()
		}
		return r.url, r.res, nil
	}
	return "", nil, errNoMirrorReply
}

// readBilibiliRange is readBilibiliDirectRange: three attempts over the ordered mirrors, the first one hedged, and
// a body that resumes on another mirror or switches to a faster one.
func (s *Server) readBilibiliRange(ctx context.Context, primary string, alternates []string, rng string) (*mediaReply, *commentErr) {
	mirrors := bilibiliMirrorOrder(primary, alternates)
	if subchunksEnabled() {
		if start, end, ok := splitRangeHeader(rng); ok && start >= 0 && (end < 0 || start <= end && end-start+1 > subchunkSize) {
			return s.readBilibiliSubchunked(ctx, mirrors, start, end)
		}
	}
	last := "Proxy fetch failed"
	for attempt := 1; attempt <= bilibiliRangeAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, &commentErr{499, "", ""}
		}
		listed := mirrors[(attempt-1)%len(mirrors)]
		hedge := ""
		if attempt == 1 {
			for _, m := range mirrors {
				if m != listed {
					hedge = m
					break
				}
			}
		}
		started := time.Now()
		used, res, err := s.hedgedOpen(ctx, listed, hedge, rng)
		if err != nil {
			mirrorTracker.failure(listed)
			last = err.Error()
			continue
		}
		if !successful(res) {
			mirrorTracker.failure(used)
			last = "Upstream returned " + strconv.Itoa(res.StatusCode)
			res.Body.Close()
			continue
		}
		opened := time.Since(started)
		mirrorTracker.success(used, opened.Milliseconds())
		var others []string
		for _, m := range mirrors {
			if m != used {
				others = append(others, m)
			}
		}
		body := newRetryingBody(s, ctx, used, rng, res, bilibiliRangeAttempts-attempt, others, opened)
		ct := res.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		length := ""
		if res.ContentLength >= 0 {
			length = strconv.FormatInt(res.ContentLength, 10)
		}
		return &mediaReply{status: res.StatusCode, ctype: ct, length: length, crange: res.Header.Get("Content-Range"),
			accept: res.Header.Get("Accept-Ranges"), body: body}, nil
	}
	return nil, &commentErr{http.StatusUnprocessableEntity, last, "error"}
}

// retryingBody is RetryingProxyInputStream: it resumes a body cut mid-way from the bytes already sent, on the same
// or another mirror, checks the overlap (or the replayed prefix) so a changed file is never spliced, and moves to a
// clearly faster mirror when the current one crawls.
type retryingBody struct {
	s             *Server
	ctx           context.Context
	url, cur      string
	rng           string
	res           *http.Response
	src           io.Reader
	attempts      int
	resumeURLs    []string
	status        int
	length        int64
	header        map[string]string
	crange        *contentRange
	digest        hash.Hash
	delivered     int64
	resumes       int
	tail          []byte
	wait, xfer    time.Duration
	closed, eof   bool
	switchChecked bool
}

func newRetryingBody(s *Server, ctx context.Context, u, rng string, res *http.Response, attempts int, others []string, opened time.Duration) *retryingBody {
	b := &retryingBody{s: s, ctx: ctx, url: u, cur: u, rng: rng, res: res, src: res.Body, attempts: attempts, resumeURLs: others, status: res.StatusCode,
		length: res.ContentLength, wait: opened, header: map[string]string{}}
	for _, k := range []string{"Content-Range", "Content-Type", "Content-Encoding", "ETag"} {
		b.header[k] = res.Header.Get(k)
	}
	if cr, ok := parseContentRange(b.header["Content-Range"]); ok {
		b.crange = &cr
	}
	if !strongETag(b.header["ETag"]) {
		b.digest = sha256.New()
	}
	return b
}

func strongETag(v string) bool {
	return len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`)
}

func (b *retryingBody) Read(p []byte) (int, error) {
	if b.closed {
		return 0, errors.New("proxy stream is closed")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if b.length >= 0 && b.delivered == b.length {
		b.eof = true
		return 0, io.EOF
	}
	for {
		b.maybeSwitch()
		size := len(p)
		if b.length >= 0 {
			size = int(min(int64(size), b.length-b.delivered))
		}
		started := time.Now()
		n, err := b.src.Read(p[:size])
		el := time.Since(started)
		b.wait += el
		b.xfer += el
		if n > 0 {
			if b.digest != nil {
				b.digest.Write(p[:n])
			}
			b.record(p[:n])
			b.delivered += int64(n)
			return n, nil
		}
		if err == io.EOF {
			if b.length >= 0 && b.delivered < b.length {
				err = io.ErrUnexpectedEOF
			} else {
				b.eof = true
				return 0, io.EOF
			}
		}
		if err == nil {
			continue
		}
		if b.ctx.Err() != nil {
			return 0, b.ctx.Err()
		}
		if rerr := b.recover(err); rerr != nil {
			return 0, rerr
		}
	}
}

// record keeps the last 16 KiB sent, the overlap a resumed request must repeat exactly.
func (b *retryingBody) record(p []byte) {
	const overlap = 16 << 10
	b.tail = append(b.tail, p...)
	if len(b.tail) > overlap {
		b.tail = append(b.tail[:0], b.tail[len(b.tail)-overlap:]...)
	}
}

func (b *retryingBody) resumeStart() int64 { return b.crange.start + b.delivered - int64(len(b.tail)) }

func (b *retryingBody) recover(first error) error {
	b.res.Body.Close()
	var expected []byte
	if b.digest != nil {
		expected = b.digest.Sum(nil)
	}
	last := first
	for b.attempts > 0 {
		if b.closed || b.ctx.Err() != nil {
			return last
		}
		b.attempts--
		target, rng := b.url, b.rng
		if b.crange != nil {
			mirrors := append([]string{b.url}, b.resumeURLs...)
			target = mirrors[b.resumes%len(mirrors)]
			if b.delivered > 0 {
				rng = "bytes=" + strconv.FormatInt(b.resumeStart(), 10) + "-" + strconv.FormatInt(b.crange.end, 10)
			}
		}
		b.resumes++
		res, err := b.s.proxyClient.Do(bilibiliRequest(b.ctx, target, rng))
		if err != nil {
			last = err
			continue
		}
		if !successful(res) {
			res.Body.Close()
			last = errors.New("upstream returned " + strconv.Itoa(res.StatusCode))
			continue
		}
		sameHost := hostOf(target) == hostOf(b.url)
		if b.crange != nil && b.delivered > 0 && res.StatusCode == http.StatusPartialContent {
			if got, ok := parseContentRange(res.Header.Get("Content-Range")); ok && got == (contentRange{b.resumeStart(), b.crange.end, b.crange.total}) &&
				res.ContentLength == b.crange.end-b.resumeStart()+1 && res.Header.Get("Content-Encoding") == b.header["Content-Encoding"] &&
				(!sameHost || strings.EqualFold(res.Header.Get("ETag"), b.header["ETag"])) {
				over := make([]byte, len(b.tail))
				if _, err := io.ReadFull(res.Body, over); err != nil {
					res.Body.Close()
					last = err
					continue
				}
				if !bytes.Equal(over, b.tail) {
					res.Body.Close()
					return errChangedBody
				}
				b.res, b.src, b.cur = res, res.Body, target
				return nil
			}
		}
		etagOK := !sameHost || strings.EqualFold(res.Header.Get("ETag"), b.header["ETag"])
		if res.StatusCode != b.status || res.ContentLength != b.length || !etagOK || res.Header.Get("Content-Range") != b.header["Content-Range"] ||
			res.Header.Get("Content-Type") != b.header["Content-Type"] || res.Header.Get("Content-Encoding") != b.header["Content-Encoding"] {
			res.Body.Close()
			return errChangedBody
		}
		var next hash.Hash
		if expected != nil {
			next = sha256.New()
		}
		check := sha256.New()
		if _, err := io.CopyN(io.MultiWriter(writerOrDiscard(next), check), res.Body, b.delivered); err != nil {
			res.Body.Close()
			last = err
			continue
		}
		if expected != nil && !bytes.Equal(expected, check.Sum(nil)) {
			res.Body.Close()
			return errChangedBody
		}
		b.res, b.src, b.digest, b.cur = res, res.Body, next, target
		return nil
	}
	return last
}

func writerOrDiscard(h hash.Hash) io.Writer {
	if h == nil {
		return io.Discard
	}
	return h
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Hostname()
}

func (b *retryingBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	if b.eof && b.delivered >= 256<<10 && b.xfer > 0 {
		mirrorTracker.throughput(b.cur, float64(b.delivered)*1e9/float64(b.xfer.Nanoseconds()))
	}
	return b.res.Body.Close()
}
