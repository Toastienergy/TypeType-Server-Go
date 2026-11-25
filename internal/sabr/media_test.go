package sabr

import (
	"bufio"
	"bytes"
	"context"
	"runtime"
	"testing"
	"time"
)

func readSegments(t *testing.T, s *Session, segments int) {
	t.Helper()
	stream, _ := umpStream(segments)
	var cookie []byte
	var out roundResult
	if err := s.readUMP(bufio.NewReader(bytes.NewReader(stream)), s.info, &cookie, &out); err != nil {
		t.Fatal(err)
	}
}

func mediaSession() *Session {
	s := &Session{changed: make(chan struct{}), info: &Info{}, cancel: func() {}, store: &Store{sessions: map[string]*Session{}}}
	s.tracks[videoTrack], s.tracks[audioTrack] = newTrack(&Format{Itag: 137}), newTrack(&Format{Itag: 140})
	return s
}

func TestEvictedSegmentStaysReadableUntilReleased(t *testing.T) {
	base := mappedBytes.Load()
	s := mediaSession()
	readSegments(t, s, 4)
	if got := mappedBytes.Load() - base; got < 4*8*16<<10 {
		t.Fatalf("%d bytes mapped for four segments", got)
	}
	seg, ok := s.Media(context.Background(), 140, 1, time.Millisecond)
	if !ok {
		t.Fatal("segment 1 missing")
	}
	before := bytes.Clone(seg.Data())
	s.mu.Lock()
	s.playerMs = 1 << 40
	s.evictLocked()
	s.mu.Unlock()
	if len(before) != 8*16<<10 || !bytes.Equal(seg.Data(), before) {
		t.Fatal("held segment changed after eviction")
	}
	if got := mappedBytes.Load() - base; got != int64(cap(seg.Data())) {
		t.Fatalf("%d bytes still mapped, want only the held segment", got)
	}
	seg.Release()
	if got := mappedBytes.Load() - base; got != 0 {
		t.Fatalf("%d bytes still mapped after the last release", got)
	}
}

func TestReleasedSessionFreesItsMediaAndRefusesNewSegments(t *testing.T) {
	base := mappedBytes.Load()
	s := mediaSession()
	readSegments(t, s, 3)
	s.store.Release(s)
	if got := mappedBytes.Load() - base; got != 0 || s.bytes != 0 {
		t.Fatalf("%d bytes mapped, %d counted after release", got, s.bytes)
	}
	readSegments(t, s, 2)
	if got := mappedBytes.Load() - base; got != 0 || len(s.tracks[audioTrack].segs) != 0 {
		t.Fatalf("%d bytes mapped after the pump finished segments for a released session", got)
	}
}

func TestDroppedSegmentIsUnmappedByItsCleanup(t *testing.T) {
	base := mappedBytes.Load()
	func() {
		seg := newSegment(1, 0, 1000, 256<<10)
		seg.grow(512 << 10)
	}()
	for i := 0; i < 20 && mappedBytes.Load() != base; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if got := mappedBytes.Load() - base; got != 0 {
		t.Fatalf("%d bytes still mapped after the segment became unreachable", got)
	}
}

func TestRestartedPartReleasesTheUnfinishedSegment(t *testing.T) {
	base := mappedBytes.Load()
	var b bytes.Buffer
	var h pw
	h.varint(1, 7)
	h.varint(3, 140)
	h.varint(9, 1)
	h.varint(11, 0)
	h.varint(12, 10000)
	h.varint(14, 128<<10)
	umpPart(&b, 20, h.b)
	umpPart(&b, 21, append([]byte{7}, make([]byte, 16<<10)...))
	umpPart(&b, 20, h.b)
	s := mediaSession()
	var cookie []byte
	var out roundResult
	if err := s.readUMP(bufio.NewReader(&b), s.info, &cookie, &out); err != nil {
		t.Fatal(err)
	}
	if got := mappedBytes.Load() - base; got != 0 {
		t.Fatalf("%d bytes still mapped for parts that never finished", got)
	}
}
