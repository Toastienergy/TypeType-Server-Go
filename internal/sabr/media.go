package sabr

import (
	"runtime"
	"slices"
)

// newSegment starts a media segment whose bytes come from mediaAlloc: on Linux an anonymous mapping outside the Go
// heap, handed back to the system as soon as the last holder releases it instead of waiting for a GC cycle. The
// session holds the first reference; Session.Media adds one per request still writing the bytes out.
func newSegment(seq int, startMs, durMs int64, size int) *Segment {
	s := &Segment{Seq: seq, StartMs: startMs, DurMs: durMs}
	s.setData(mediaAlloc(size))
	s.refs.Store(1)
	return s
}

// newInitSegment keeps its bytes on the Go heap: the track keeps them for the whole session without a reference.
func newInitSegment(size int) *Segment {
	return &Segment{data: make([]byte, 0, size), heap: true}
}

// grow makes room for n more bytes when YouTube sent a wrong or no content length.
func (s *Segment) grow(n int) {
	if cap(s.data)-len(s.data) >= n {
		return
	}
	if s.heap {
		s.data = slices.Grow(s.data, n)
		return
	}
	old := s.data
	next := mediaAlloc(max(2*cap(old), len(old)+n))[:len(old)]
	copy(next, old)
	s.setData(next)
}

// setData swaps the bytes, frees the previous ones and arms a cleanup so a segment dropped without Release still
// gives its mapping back once the collector finds it unreachable.
func (s *Segment) setData(b []byte) {
	if s.armed {
		s.cleanup.Stop()
		s.armed = false
	}
	if s.data != nil {
		mediaFree(s.data)
	}
	s.data = b
	if cap(b) >= mappedMin {
		s.cleanup, s.armed = runtime.AddCleanup(s, mediaFree, b), true
	}
}

func (s *Segment) retain() { s.refs.Add(1) }

// Release drops one reference; the last one frees the bytes, so Data must not be read after it.
func (s *Segment) Release() {
	if s.heap {
		return
	}
	if s.refs.Add(-1) == 0 {
		s.setData(nil)
	}
}

// dropMediaLocked releases every segment once the store let the session go; requests still writing one keep it
// until they finish, and the pump releases what it finishes later.
func (s *Session) dropMediaLocked() {
	s.released = true
	for _, t := range s.tracks {
		if t == nil {
			continue
		}
		for k, sg := range t.segs {
			delete(t.segs, k)
			sg.Release()
		}
		for _, sg := range t.live {
			s.loseLocked(t, sg)
		}
		t.bytes = 0
	}
	s.bytes = 0
}
