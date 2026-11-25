package sabr

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// liveFeed starts the pump's reader on a pipe and returns the writer: the test sends one segment's parts as
// YouTube would, a part at a time.
func liveFeed(t *testing.T, s *Session) (*io.PipeWriter, chan error) {
	t.Helper()
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() {
		var cookie []byte
		var out roundResult
		done <- s.readUMP(bufio.NewReader(r), s.info, &cookie, &out)
	}()
	return w, done
}

func segmentHeader(seq int, size int) []byte {
	var b bytes.Buffer
	var h pw
	h.varint(1, uint64(seq))
	h.varint(3, 140)
	h.varint(9, uint64(seq))
	h.varint(11, 0)
	h.varint(12, 10000)
	h.varint(14, uint64(size))
	umpPart(&b, 20, h.b)
	return b.Bytes()
}

func segmentChunk(seq int, fill byte, n int) []byte {
	var b bytes.Buffer
	umpPart(&b, 21, append([]byte{byte(seq)}, bytes.Repeat([]byte{fill}, n)...))
	return b.Bytes()
}

func liveSession() *Session {
	s := mediaSession()
	s.Video, s.Audio = s.tracks[videoTrack].f, s.tracks[audioTrack].f
	return s
}

func waitLive(t *testing.T, s *Session) *Segment {
	t.Helper()
	seg, live, ok := s.MediaLive(context.Background(), 140, 1, time.Second)
	if !ok || !live {
		t.Fatalf("segment not offered while it arrives: ok=%v live=%v", ok, live)
	}
	return seg
}

func TestSegmentIsReadWhileItArrives(t *testing.T) {
	s := liveSession()
	w, pumped := liveFeed(t, s)
	w.Write(segmentHeader(1, 3000))
	w.Write(segmentChunk(1, 'a', 1000))
	seg := waitLive(t, s)
	defer seg.Release()
	win := s.BuildWindow(&WindowRequest{PlaybackRate: 1, BufferGoalMs: 30000, AudioOnly: true})
	if !win.Ready || len(win.Audio.Segments) != 1 {
		t.Fatalf("window does not offer the arriving segment: %+v", win)
	}
	got := make(chan []byte, 8)
	followed := make(chan error, 1)
	go func() {
		followed <- s.Follow(context.Background(), seg, func(b []byte) error {
			got <- bytes.Clone(b)
			return nil
		})
	}()
	if first := <-got; len(first) != 1000 || first[0] != 'a' {
		t.Fatalf("first read got %d bytes before the segment ended", len(first))
	}
	w.Write(segmentChunk(1, 'b', 2000))
	var end bytes.Buffer
	umpPart(&end, 22, []byte{1})
	w.Write(end.Bytes())
	if err := <-followed; err != nil {
		t.Fatal(err)
	}
	close(got)
	total := 1000
	for b := range got {
		total += len(b)
	}
	if total != 3000 {
		t.Fatalf("followed %d bytes, want 3000", total)
	}
	w.Close()
	if err := <-pumped; err != nil {
		t.Fatal(err)
	}
	if whole, ok := s.Media(context.Background(), 140, 1, time.Millisecond); !ok || len(whole.Data()) != 3000 {
		t.Fatal("finished segment not kept")
	} else {
		whole.Release()
	}
}

func TestCutResponseFailsTheReader(t *testing.T) {
	s := liveSession()
	w, pumped := liveFeed(t, s)
	w.Write(segmentHeader(1, 3000))
	w.Write(segmentChunk(1, 'a', 1000))
	seg := waitLive(t, s)
	defer seg.Release()
	followed := make(chan error, 1)
	go func() { followed <- s.Follow(context.Background(), seg, func([]byte) error { return nil }) }()
	w.CloseWithError(io.ErrUnexpectedEOF)
	<-pumped
	if err := <-followed; !errors.Is(err, ErrSegmentLost) {
		t.Fatalf("reader of a cut segment got %v", err)
	}
	if _, live, ok := s.MediaLive(context.Background(), 140, 1, time.Millisecond); ok || live {
		t.Fatal("a lost segment is still offered")
	}
}
