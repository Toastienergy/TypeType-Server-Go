package sabr

import "testing"

func TestCoveringAcrossRoundedBoundary(t *testing.T) {
	tr := newTrack(&Format{Itag: 140})
	tr.segs[3] = &Segment{Seq: 3, StartMs: 29953, DurMs: 9984, done: true}
	tr.segs[4] = &Segment{Seq: 4, StartMs: 39938, DurMs: 9984, done: true}
	if s := tr.covering(39937); s == nil || s.Seq != 4 {
		t.Fatalf("covering(39937) = %+v, want seq 4", s)
	}
	if s := tr.covering(35000); s == nil || s.Seq != 3 {
		t.Fatalf("covering(35000) = %+v, want seq 3", s)
	}
	if end, first, last := tr.coveredFrom(30000); end != 49922 || first != 3 || last != 4 {
		t.Fatalf("coveredFrom = %d %d %d", end, first, last)
	}
}
