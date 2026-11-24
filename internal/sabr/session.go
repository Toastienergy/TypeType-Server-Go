package sabr

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Segment is one media segment as YouTube numbered it. data is written once by the pump, then only read, so
// handlers send it without copying.
type Segment struct {
	Seq     int
	StartMs int64
	DurMs   int64
	data    []byte
	done    bool
}

func (s *Segment) EndMs() int64 { return s.StartMs + s.DurMs }
func (s *Segment) Data() []byte { return s.data }

type track struct {
	f      *Format
	init   []byte
	inited bool
	segs   map[int]*Segment
	endSeq int
	bytes  int64
}

func newTrack(f *Format) *track { return &track{f: f, segs: map[int]*Segment{}} }

// boundaryMs absorbs the rounding of segment times to whole milliseconds: one segment may end a millisecond or
// two before the next one starts, and a client resuming at the first end must land on the next.
const boundaryMs = 20

// covering returns the finished segment that plays at ms, if cached.
func (t *track) covering(ms int64) *Segment {
	var best *Segment
	for _, s := range t.segs {
		if s.done && s.StartMs <= ms+boundaryMs && ms < s.EndMs() && (best == nil || s.Seq > best.Seq) {
			best = s
		}
	}
	return best
}

// coveredFrom is where the contiguous run of finished segments playing from ms ends (ms itself when nothing
// plays there), and the sequence range of that run.
func (t *track) coveredFrom(ms int64) (end int64, first, last int) {
	s := t.covering(ms)
	if s == nil {
		return ms, 0, 0
	}
	first, last, end = s.Seq, s.Seq, s.EndMs()
	for {
		n := t.segs[last+1]
		if n == nil || !n.done {
			return end, first, last
		}
		last, end = n.Seq, n.EndMs()
	}
}

func (t *track) atEnd(ms int64) bool {
	if t.endSeq == 0 {
		return false
	}
	last := t.segs[t.endSeq]
	return last != nil && last.done && ms >= last.EndMs()
}

// Failure kinds, matching SabrFailureKind.
const (
	FailUpstreamRefused = "upstream_refused"
	FailSessionChanged  = "provider_session_changed"
	FailUpstreamTimeout = "upstream_timeout"
	FailContentMissing  = "content_unavailable"
	FailUnknown         = "unknown"
)

// Session is one playback: the chosen formats, the generation the client plays, where it plays, and the segments
// the pump fetched around that point.
type Session struct {
	ID        string
	VideoID   string
	UserID    string
	Video     *Format
	Audio     *Format
	AudioOnly bool

	store *Store
	mu    sync.Mutex
	info  *Info
	gen   int64
	// playerMs is where the client plays; demandMs a later point a window or segment request waits for.
	playerMs     int64
	demandMs     int64
	seekPending  bool
	tracks       [2]*track
	changed      chan struct{}
	failKind     string
	failMsg      string
	lastUsed     time.Time
	cancel       context.CancelFunc
	abortRequest context.CancelFunc
	wake         chan struct{}
	bytes        int64
	readyOnce    bool
}

const (
	videoTrack = 0
	audioTrack = 1
)

func (s *Session) Generation() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

func (s *Session) PlayerMs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playerMs
}

// Failure is the terminal failure, if any.
func (s *Session) Failure() (kind, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failKind, s.failMsg
}

func (s *Session) touch() { s.lastUsed = time.Now() }

// notify wakes every waiter; callers hold s.mu.
func (s *Session) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Session) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Fail ends the session with a failure kind; waiters wake up and the pump stops.
func (s *Session) Fail(kind, msg string) { s.fail(kind, msg) }

func (s *Session) fail(kind, msg string) {
	s.mu.Lock()
	if s.failKind == "" {
		s.failKind, s.failMsg = kind, msg
	}
	s.notify()
	s.mu.Unlock()
}

// SetPosition records where the client plays, for the generation it plays.
func (s *Session) SetPosition(gen, playerMs int64, audioOnly bool) {
	s.mu.Lock()
	if gen == s.gen {
		s.playerMs = max(0, playerMs)
		s.AudioOnly = audioOnly
		s.touch()
	}
	s.mu.Unlock()
	s.poke()
}

// SeekTo moves the session to a new generation at playerMs. Cached segments stay: a seek back into them is served
// at once. The request in flight is dropped unless it already feeds the target.
func (s *Session) SeekTo(playerMs int64, audioOnly bool) int64 {
	s.mu.Lock()
	s.gen++
	s.playerMs, s.demandMs, s.AudioOnly = max(0, playerMs), 0, audioOnly
	s.seekPending = true
	s.touch()
	gen := s.gen
	abort := s.abortRequest
	s.notify()
	s.mu.Unlock()
	if abort != nil {
		abort()
	}
	s.poke()
	return gen
}

func (s *Session) activeTracks() []int {
	if s.AudioOnly {
		return []int{audioTrack}
	}
	return []int{videoTrack, audioTrack}
}

// Init waits for the initialization segment of itag.
func (s *Session) Init(ctx context.Context, itag int, timeout time.Duration) ([]byte, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		t := s.trackFor(itag)
		if t == nil {
			s.mu.Unlock()
			return nil, false
		}
		if t.inited {
			b := t.init
			s.mu.Unlock()
			return b, true
		}
		ch := s.changed
		failed := s.failKind != ""
		s.mu.Unlock()
		if failed {
			return nil, false
		}
		select {
		case <-ch:
		case <-deadline.C:
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
	}
}

func (s *Session) trackFor(itag int) *track {
	for _, t := range s.tracks {
		if t != nil && t.f.Itag == itag {
			return t
		}
	}
	return nil
}

// Media waits for segment seq of itag; when it is not cached it asks the pump for the time it should play at.
func (s *Session) Media(ctx context.Context, itag, seq int, timeout time.Duration) (*Segment, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	asked := false
	for {
		s.mu.Lock()
		t := s.trackFor(itag)
		if t == nil {
			s.mu.Unlock()
			return nil, false
		}
		if seg := t.segs[seq]; seg != nil && seg.done {
			s.touch()
			s.mu.Unlock()
			return seg, true
		}
		if !asked {
			asked = true
			if at, ok := t.estimate(seq); ok && at > s.playerMs {
				s.demandMs = at
			}
		}
		ch := s.changed
		failed := s.failKind != ""
		s.mu.Unlock()
		if failed {
			return nil, false
		}
		s.poke()
		select {
		case <-ch:
		case <-deadline.C:
			return nil, false
		case <-ctx.Done():
			return nil, false
		}
	}
}

// estimate guesses when a sequence not yet fetched plays, from the nearest cached one.
func (t *track) estimate(seq int) (int64, bool) {
	keys := make([]int, 0, len(t.segs))
	for k, sg := range t.segs {
		if sg.done {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return 0, false
	}
	sort.Ints(keys)
	i := sort.SearchInts(keys, seq)
	if i == len(keys) {
		i--
	}
	ref := t.segs[keys[i]]
	if ref.DurMs <= 0 {
		return ref.StartMs, true
	}
	return max(0, ref.StartMs+int64(seq-ref.Seq)*ref.DurMs), true
}

// DurationMs is the indexed end of both tracks when known, else the longest approximate duration.
func (s *Session) DurationMs() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.durationLocked()
}

func (s *Session) durationLocked() int64 {
	var ends []int64
	for _, t := range s.tracks {
		if t.endSeq > 0 {
			if last := t.segs[t.endSeq]; last != nil && last.done {
				ends = append(ends, last.EndMs())
			}
		}
	}
	if len(ends) == 2 {
		return min(ends[0], ends[1])
	}
	return max(s.Video.DurationMs.Int(), s.Audio.DurationMs.Int(), s.info.DurationMs, 0)
}

// Wait blocks until the session changes or the timeout passes.
func (s *Session) Wait(ctx context.Context, timeout time.Duration) {
	s.mu.Lock()
	ch := s.changed
	s.mu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-ctx.Done():
	}
}

// Stats is what the state, position and preparing answers report.
type Stats struct {
	Generation  int64
	PlayerMs    int64
	ReaderHead  int64
	ReaderTail  int64
	CachedBytes int64
	FailKind    string
	FailMsg     string
	Demand      int64
}

func (s *Session) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{Generation: s.gen, PlayerMs: s.playerMs, CachedBytes: s.bytes, FailKind: s.failKind, FailMsg: s.failMsg, Demand: s.demandMs}
	head := int64(-1)
	tail := int64(-1)
	for _, i := range s.activeTracks() {
		t := s.tracks[i]
		end, _, _ := t.coveredFrom(s.playerMs)
		if head < 0 || end < head {
			head = end
		}
		for _, sg := range t.segs {
			if sg.done && (tail < 0 || sg.StartMs < tail) {
				tail = sg.StartMs
			}
		}
	}
	st.ReaderHead, st.ReaderTail = max(head, s.playerMs), max(tail, 0)
	return st
}
