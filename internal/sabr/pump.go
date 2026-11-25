package sabr

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"time"
)

const (
	leadMs       = 10000
	backBufferMs = 30000
	maxEmpty     = 6
)

func cpn() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	b := make([]byte, 16)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[b[i]&63]
	}
	return string(b)
}

type pending struct {
	t    *track
	seg  *Segment
	init bool
	skip bool
}

type roundResult struct {
	media    int
	backoff  time.Duration
	reload   string
	fatal    string
	failKind string
}

func formatID(f *Format) []byte {
	var w pw
	w.varint(1, uint64(f.Itag))
	w.varint(2, f.lmt)
	w.str(3, f.Xtags)
	return w.b
}

type rangeSpec struct {
	f           *Format
	end         int64
	first, last int
}

func buildRequest(in *Info, video, audio *Format, inited [2]bool, ranges []rangeSpec, playerMs int64, cookie []byte, audioOnly bool) []byte {
	var st pw
	st.varint(16, uint64(video.Height))
	st.varint(21, uint64(video.Height))
	st.varint(23, uint64(video.Bitrate+audio.Bitrate)*2)
	st.varint(28, uint64(playerMs))
	st.varint(34, 1)
	st.fixed32(35, math.Float32bits(1))
	if audioOnly {
		st.varint(40, 1)
	}
	var req pw
	req.bytes(1, st.b)
	if inited[videoTrack] && !audioOnly {
		req.bytes(2, formatID(video))
	}
	if inited[audioTrack] {
		req.bytes(2, formatID(audio))
	}
	for _, r := range ranges {
		var b pw
		b.bytes(1, formatID(r.f))
		b.varint(2, 0)
		b.varint(3, uint64(r.end))
		b.varint(4, uint64(r.first))
		b.varint(5, uint64(r.last))
		req.bytes(3, b.b)
	}
	req.bytes(5, in.Ustreamer)
	req.bytes(16, formatID(audio))
	if !audioOnly {
		req.bytes(17, formatID(video))
	}
	var client pw
	client.varint(16, uint64(in.ClientID))
	client.str(17, in.ClientVer)
	var sc pw
	sc.bytes(1, client.b)
	if len(in.PoToken) > 0 {
		sc.bytes(2, in.PoToken)
	}
	if cookie != nil {
		sc.bytes(3, cookie)
	}
	req.bytes(19, sc.b)
	return req.b
}

// run keeps the cache filled from where the client plays (or waits) up to leadMs ahead, drops what is far behind,
// and follows seeks, redirects, reloads and backoffs, until the session closes or fails.
func (s *Session) run(ctx context.Context) {
	nonce, rn, empty := cpn(), 0, 0
	var cookie []byte
	for ctx.Err() == nil {
		s.mu.Lock()
		if s.failKind != "" {
			s.mu.Unlock()
			return
		}
		anchor := s.playerMs
		if s.demandMs > anchor {
			anchor = s.demandMs
		}
		reqMs := int64(math.MaxInt64)
		var ranges []rangeSpec
		active := s.activeTracks()
		for _, i := range active {
			t := s.tracks[i]
			end, first, last := t.coveredFrom(anchor)
			if !t.atEnd(end) {
				reqMs = min(reqMs, end)
			}
			if last > 0 {
				ranges = append(ranges, rangeSpec{f: t.f, end: end, first: first, last: last})
			}
		}
		if s.demandMs > 0 && reqMs > s.demandMs+leadMs/2 {
			s.demandMs = 0
		}
		idle := reqMs == math.MaxInt64 || reqMs-s.playerMs >= leadMs && s.demandMs == 0
		if idle {
			s.evictLocked()
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			case <-time.After(time.Second):
			}
			continue
		}
		if s.seekPending {
			cookie, s.seekPending = nil, false
		}
		in := s.info
		inited := [2]bool{s.tracks[videoTrack].inited, s.tracks[audioTrack].inited}
		body := buildRequest(in, s.Video, s.Audio, inited, ranges, reqMs, cookie, s.AudioOnly)
		reqCtx, cancel := context.WithCancel(ctx)
		s.abortRequest = cancel
		s.mu.Unlock()

		res := s.round(reqCtx, in, body, nonce, rn, &cookie)
		rn++
		s.requests.Store(int64(rn))
		cancel()
		s.mu.Lock()
		s.abortRequest = nil
		s.evictLocked()
		s.mu.Unlock()
		switch {
		case ctx.Err() != nil:
			return
		case res.fatal != "":
			s.fail(res.failKind, res.fatal)
			return
		case res.reload != "":
			next, err := s.store.token.Reload(ctx, s.VideoID, res.reload)
			if err != nil {
				s.fail(FailSessionChanged, "reload: "+err.Error())
				return
			}
			s.mu.Lock()
			s.info = next
			s.mu.Unlock()
			nonce, rn, cookie = cpn(), 0, nil
			continue
		}
		if res.media == 0 && reqCtx.Err() == nil {
			empty++
			if empty >= maxEmpty {
				s.fail(FailUpstreamRefused, "YouTube sent no media for "+strconv.Itoa(maxEmpty)+" requests")
				return
			}
		} else {
			empty = 0
		}
		if res.backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(res.backoff):
			}
		}
	}
}

// round sends one SABR request and stores what comes back. A cancelled request (seek) is not an error.
func (s *Session) round(ctx context.Context, in *Info, body []byte, nonce string, rn int, cookie *[]byte) roundResult {
	var out roundResult
	sep := "?"
	if strings.Contains(in.BaseURL, "?") {
		sep = "&"
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", in.BaseURL+sep+"alr=yes&cpn="+nonce+"&rn="+strconv.Itoa(rn), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Accept", "application/vnd.yt-ump")
	req.Header.Set("User-Agent", in.UserAgent)
	if rn < 2 {
		t := &RoundTiming{Round: rn}
		began := time.Now()
		since := func() int64 { return time.Since(began).Milliseconds() }
		req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			GotConn:              func(i httptrace.GotConnInfo) { t.Reused, t.ConnMs = i.Reused, since() },
			TLSHandshakeDone:     func(tls.ConnectionState, error) { t.TLSMs = since() },
			GotFirstResponseByte: func() { t.FirstByteMs = since() },
		}))
		defer func() {
			t.DoneMs, t.Media, t.BackoffMs, t.Reload = since(), out.media, out.backoff.Milliseconds(), out.reload != ""
			s.firstRoundDone(t)
		}()
	}
	resp, err := s.store.media.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			out.fatal, out.failKind = "googlevideo: "+err.Error(), FailUpstreamTimeout
		}
		return out
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 160))
		out.fatal, out.failKind = "googlevideo HTTP 403 from "+req.URL.Hostname()+": "+strings.TrimSpace(string(snippet)), FailUpstreamRefused
		return out
	case resp.StatusCode != http.StatusOK:
		out.fatal, out.failKind = fmt.Sprintf("googlevideo HTTP %d", resp.StatusCode), FailUnknown
		return out
	}
	err = s.readUMP(bufio.NewReaderSize(resp.Body, 64<<10), in, cookie, &out)
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		out.fatal, out.failKind = "googlevideo: "+err.Error(), FailUpstreamTimeout
	}
	return out
}

func umpInt(r *bufio.Reader) (int, error) {
	first, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	extra := 0
	switch {
	case first < 128:
		return int(first), nil
	case first < 192:
		extra = 1
	case first < 224:
		extra = 2
	case first < 240:
		extra = 3
	default:
		extra = 4
	}
	value, shift := 0, 0
	if extra < 4 {
		value = int(first) & (0xff >> (extra + 1))
		shift = 8 - (extra + 1)
	}
	for i := 0; i < extra; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value += int(b) << (shift + 8*i)
	}
	return value, nil
}

type mediaHeader struct {
	id, itag, seq  int
	init           bool
	startMs, durMs int64
	contentLength  int
}

func parseHeader(data []byte) mediaHeader {
	var h mediaHeader
	fields(data, func(num int, v uint64, raw []byte) {
		switch num {
		case 1:
			h.id = int(v)
		case 3:
			h.itag = int(v)
		case 8:
			h.init = v != 0
		case 9:
			h.seq = int(v)
		case 11:
			h.startMs = int64(v)
		case 12:
			h.durMs = int64(v)
		case 13:
			if h.itag == 0 {
				h.itag = int(varintField(raw, 1))
			}
		case 14:
			h.contentLength = int(v)
		case 15:
			var start, dur, scale uint64
			fields(raw, func(n int, v uint64, _ []byte) {
				switch n {
				case 1:
					start = v
				case 2:
					dur = v
				case 3:
					scale = v
				}
			})
			if scale > 0 {
				h.startMs, h.durMs = int64(start*1000/scale), int64(dur*1000/scale)
			}
		}
	})
	return h
}

func (s *Session) readUMP(r *bufio.Reader, in *Info, cookie *[]byte, out *roundResult) error {
	parts := map[int]*pending{}
	defer func() {
		for _, p := range parts {
			if p != nil && p.seg != nil {
				p.seg.Release()
			}
		}
	}()
	// Control parts are read into one reused buffer: nothing below keeps a slice of it, only copies.
	var scratch []byte
	var free []*pending
	for {
		typ, err := umpInt(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		size, err := umpInt(r)
		if err != nil {
			return err
		}
		if typ == 21 && size > 0 {
			id, err := r.ReadByte()
			if err != nil {
				return err
			}
			p := parts[int(id)]
			if p == nil || p.skip {
				if _, err := r.Discard(size - 1); err != nil {
					return err
				}
				continue
			}
			n := size - 1
			p.seg.grow(n)
			buf := p.seg.data
			if _, err := io.ReadFull(r, buf[len(buf):len(buf)+n]); err != nil {
				return err
			}
			p.seg.data = buf[:len(buf)+n]
			continue
		}
		if cap(scratch) < size {
			scratch = make([]byte, max(size, 512))
		}
		data := scratch[:size]
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		switch typ {
		case 20:
			h := parseHeader(data)
			var p *pending
			if n := len(free); n > 0 {
				p, free = free[n-1], free[:n-1]
				*p = pending{init: h.init}
			} else {
				p = &pending{init: h.init}
			}
			s.mu.Lock()
			p.t = s.trackFor(h.itag)
			switch {
			case p.t == nil:
				p.skip = true
			case h.init:
				p.skip = p.t.inited
			default:
				old := p.t.segs[h.seq]
				p.skip = old != nil && old.done
			}
			s.mu.Unlock()
			switch {
			case p.skip:
			case h.init:
				p.seg = newInitSegment(max(h.contentLength, 0))
			default:
				p.seg = newSegment(h.seq, h.startMs, h.durMs, max(h.contentLength, 0))
			}
			if old := parts[h.id]; old != nil && old.seg != nil {
				old.seg.Release()
			}
			parts[h.id] = p
		case 22:
			if len(data) == 0 {
				continue
			}
			p := parts[int(data[0])]
			delete(parts, int(data[0]))
			if p == nil {
				continue
			}
			if p.skip {
				free = append(free, p)
				continue
			}
			s.mu.Lock()
			if p.init {
				if !p.t.inited {
					p.t.init, p.t.inited = p.seg.data, true
				}
			} else if old := p.t.segs[p.seg.Seq]; !s.released && (old == nil || !old.done) {
				p.seg.done = true
				p.t.segs[p.seg.Seq] = p.seg
				p.t.bytes += int64(len(p.seg.data))
				s.bytes += int64(len(p.seg.data))
				out.media++
			} else {
				p.seg.Release()
			}
			s.notify()
			s.mu.Unlock()
			free = append(free, p)
		case 35:
			fields(data, func(num int, v uint64, raw []byte) {
				switch num {
				case 7:
					*cookie = append([]byte(nil), raw...)
				case 4:
					out.backoff = time.Duration(v) * time.Millisecond
				}
			})
		case 42:
			itag := int(varintField(bytesField(data, 2), 1))
			total := int(varintField(data, 4))
			s.mu.Lock()
			if t := s.trackFor(itag); t != nil && total > 0 {
				t.endSeq = total
			}
			s.mu.Unlock()
		case 43:
			if u := bytesField(data, 1); len(u) > 0 {
				s.mu.Lock()
				next := *s.info
				next.BaseURL = string(u)
				s.info = &next
				s.mu.Unlock()
			}
		case 44:
			out.fatal, out.failKind = fmt.Sprintf("SABR error %s code %d", bytesField(data, 1), varintField(data, 2)), FailUpstreamRefused
		case 46:
			out.reload = string(bytesField(bytesField(data, 1), 1))
		case 58:
			status := varintField(data, 1)
			s.noteProtection(int32(status))
			if status == 3 {
				out.fatal, out.failKind = "attestation required", FailUpstreamRefused
			}
		}
	}
}

// evictLocked drops what plays more than backBufferMs behind the player and, past the store budget, the oldest
// segments behind it first.
func (s *Session) evictLocked() {
	floor := s.playerMs - backBufferMs
	for _, t := range s.tracks {
		for k, sg := range t.segs {
			if sg.done && sg.EndMs() < floor {
				t.bytes -= int64(len(sg.data))
				s.bytes -= int64(len(sg.data))
				delete(t.segs, k)
				sg.Release()
			}
		}
	}
}
