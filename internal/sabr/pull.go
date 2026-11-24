package sabr

import (
	"context"
	"time"
)

// SegTime is where one segment starts and how long it lasts.
type SegTime struct{ StartMs, DurMs int64 }

// Index times every segment of itag, for a manifest written up front: the indexed times of segments already
// fetched, the average over the format's approximate duration elsewhere, each unknown one starting where the
// previous ends. It waits for the segment count, which comes with the first response, and is nil if that does not
// arrive in time.
func (s *Session) Index(ctx context.Context, itag int, timeout time.Duration) []SegTime {
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
			out := make([]SegTime, n)
			next := int64(0)
			for i := range out {
				if sg := t.segs[i+1]; sg != nil && sg.done && sg.DurMs > 0 {
					out[i] = SegTime{sg.StartMs, sg.DurMs}
				} else {
					out[i] = SegTime{next, avg}
				}
				next = out[i].StartMs + out[i].DurMs
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

// TrackState is what SabrSessionStateHandler reports per track.
type TrackState struct {
	MaxSegment, EndSegment int
	BufferedEndMs          int64
}

func (s *Session) TrackState(itag int) TrackState {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.trackFor(itag)
	if t == nil {
		return TrackState{}
	}
	st := TrackState{EndSegment: t.endSeq}
	for k, sg := range t.segs {
		if sg.done && k > st.MaxSegment {
			st.MaxSegment = k
		}
	}
	st.BufferedEndMs, _, _ = t.coveredFrom(s.playerMs)
	return st
}

// Requests is how many requests the pump sent upstream.
func (s *Session) Requests() int64 { return s.requests.Load() }

// Complete is true once every active track holds its last segment.
func (s *Session) Complete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, i := range s.activeTracks() {
		t := s.tracks[i]
		if t == nil || t.endSeq == 0 {
			return false
		}
		if last := t.segs[t.endSeq]; last == nil || !last.done {
			return false
		}
	}
	return true
}
