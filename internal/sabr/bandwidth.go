package sabr

import (
	"io"
	"time"
)

// minBandwidthSampleBytes keeps tiny answers (redirects, cookies) out of the estimate: their time is the round trip,
// not the link.
const minBandwidthSampleBytes = 64 << 10

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// noteBandwidth folds one answer into the bandwidth estimate sent to googlevideo, like PipePipe's SABR session: the
// first sample is taken as is, the next ones weigh a quarter.
func (s *Session) noteBandwidth(bytes int64, took time.Duration) {
	if bytes < minBandwidthSampleBytes || took <= 0 {
		return
	}
	sample := bytes * 8 * int64(time.Second) / int64(took)
	prev := s.bandwidth.Load()
	if prev > 0 {
		sample = (prev*3 + sample) / 4
	}
	s.bandwidth.Store(sample)
}

// bandwidthEstimate is what the next request declares: the measured estimate, or twice the formats' bitrate before
// the first measure.
func bandwidthEstimate(measured int64, video, audio *Format) uint64 {
	if measured > 0 {
		return uint64(measured)
	}
	return uint64(video.Bitrate+audio.Bitrate) * 2
}
