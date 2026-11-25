package sabr

import (
	"strings"
	"testing"
	"time"
)

func TestNoteBandwidthAveragesLikePipePipe(t *testing.T) {
	s := &Session{}
	s.noteBandwidth(1<<20, time.Second)
	if got := s.bandwidth.Load(); got != 8<<20 {
		t.Fatalf("first sample = %d, want %d", got, 8<<20)
	}
	s.noteBandwidth(1<<20, 500*time.Millisecond)
	if got, want := s.bandwidth.Load(), int64((8<<20*3+16<<20)/4); got != want {
		t.Fatalf("second sample = %d, want %d", got, want)
	}
}

func TestNoteBandwidthIgnoresTinyAnswers(t *testing.T) {
	s := &Session{}
	s.noteBandwidth(2000, time.Millisecond)
	s.noteBandwidth(1<<20, 0)
	if got := s.bandwidth.Load(); got != 0 {
		t.Fatalf("estimate = %d, want none", got)
	}
}

func TestBandwidthEstimateFallsBackToTwiceTheBitrate(t *testing.T) {
	video, audio := &Format{Bitrate: 4_000_000}, &Format{Bitrate: 128_000}
	if got := bandwidthEstimate(0, video, audio); got != 8_256_000 {
		t.Fatalf("fallback = %d", got)
	}
	if got := bandwidthEstimate(50_000_000, video, audio); got != 50_000_000 {
		t.Fatalf("measured = %d", got)
	}
}

func TestCountingReaderCounts(t *testing.T) {
	c := &countingReader{r: strings.NewReader("twelve bytes")}
	buf := make([]byte, 5)
	for {
		if _, err := c.Read(buf); err != nil {
			break
		}
	}
	if c.n != 12 {
		t.Fatalf("n = %d", c.n)
	}
}
