package sabr

import (
	"context"
	"errors"
	"time"
)

// ErrSegmentLost ends a segment followed while it arrived whose YouTube response stopped before its end: the
// client sees its request fail and asks again.
var ErrSegmentLost = errors.New("sabr: segment lost before its end")

// A media segment is listed on its track from its header on, while its bytes still arrive (track.live): a client
// asking for it gets the bytes already there, then the rest as they come, and decodes the first frames without
// waiting for the whole segment.

// liveCovering returns the segment still arriving that plays at ms.
func (t *track) liveCovering(ms int64) *Segment {
	var best *Segment
	for _, s := range t.live {
		if s.StartMs <= ms+boundaryMs && ms < s.EndMs() && (best == nil || s.Seq > best.Seq) {
			best = s
		}
	}
	return best
}

// goLive lists a segment whose header arrived; the track keeps a reference until the segment ends or is lost.
func (s *Session) goLive(t *track, seg *Segment) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return false
	}
	if old := t.live[seg.Seq]; old != nil {
		s.loseLocked(t, old)
	}
	seg.retain()
	t.live[seg.Seq] = seg
	s.notify()
	return true
}

// endLiveLocked takes a finished segment off the arriving list, telling its followers it is whole.
func (s *Session) endLiveLocked(t *track, seg *Segment) {
	if t.live[seg.Seq] != seg {
		return
	}
	delete(t.live, seg.Seq)
	seg.done = true
	seg.Release()
}

// loseLocked takes a segment whose response stopped off the arriving list, telling its followers it is lost.
func (s *Session) loseLocked(t *track, seg *Segment) {
	if t.live[seg.Seq] != seg {
		return
	}
	delete(t.live, seg.Seq)
	seg.lost = true
	seg.Release()
}

// MediaLive is Media that also returns a segment still arriving, live set; Follow writes such a segment.
func (s *Session) MediaLive(ctx context.Context, itag, seq int, timeout time.Duration) (seg *Segment, live, ok bool) {
	return s.media(ctx, itag, seq, timeout, true)
}

// Follow writes a segment still arriving: the bytes already there, then each part as it comes, until the segment
// ends (nil) or its response stops (ErrSegmentLost). Bytes are copied under the lock: the pump may move them.
func (s *Session) Follow(ctx context.Context, seg *Segment, write func([]byte) error) error {
	sent := 0
	var buf []byte
	for {
		s.mu.Lock()
		n := len(seg.data) - sent
		if n > 0 {
			buf = append(buf[:0], seg.data[sent:]...)
		}
		done, lost, changed := seg.done, seg.lost, s.changed
		s.mu.Unlock()
		if n > 0 {
			if err := write(buf); err != nil {
				return err
			}
			sent += n
			continue
		}
		switch {
		case done:
			return nil
		case lost:
			return ErrSegmentLost
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
