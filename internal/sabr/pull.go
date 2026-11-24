package sabr

import (
	"context"
	"time"
)

// Timeline is the duration of every segment of itag, for a playlist written up front: the indexed duration of a
// segment already fetched, the average over the format's approximate duration otherwise. It waits for the segment
// count, which comes with the first response, and is nil if that does not arrive in time.
func (s *Session) Timeline(ctx context.Context, itag int, timeout time.Duration) []int64 {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		t := s.trackFor(itag)
		if t == nil {
			s.mu.Unlock()
			return nil
		}
		if n := t.endSeq; n > 0 {
			avg := max(t.f.DurationMs.Int()/int64(n), 1)
			out := make([]int64, n)
			for i := range out {
				if sg := t.segs[i+1]; sg != nil && sg.done && sg.DurMs > 0 {
					out[i] = sg.DurMs
				} else {
					out[i] = avg
				}
			}
			s.mu.Unlock()
			return out
		}
		ch, failed := s.changed, s.failKind != ""
		s.mu.Unlock()
		if failed {
			return nil
		}
		s.poke()
		select {
		case <-ch:
		case <-deadline.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// Pull makes a segment request the player position, for clients that only fetch segments (an HLS player): the
// pump then fetches ahead of it and evicts behind it as it would for a player that reports its position.
func (s *Session) Pull(itag, seq int) {
	s.mu.Lock()
	t := s.trackFor(itag)
	if t == nil {
		s.mu.Unlock()
		return
	}
	at, ok := t.estimate(seq)
	if !ok && t.endSeq > 0 {
		at, ok = int64(seq-1)*t.f.DurationMs.Int()/int64(t.endSeq), true
	}
	gen, audioOnly := s.gen, s.AudioOnly
	s.mu.Unlock()
	if ok {
		s.SetPosition(gen, at, audioOnly)
	}
}
