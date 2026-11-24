package sabr

import "strconv"

const (
	maxSegmentsPerTrack = 12
	minReadyAheadMs     = 1000
)

type BufferedRange struct {
	Itag    int   `json:"itag"`
	StartMs int64 `json:"startMs"`
	EndMs   int64 `json:"endMs"`
}

type WindowRequest struct {
	Generation     int64           `json:"generation"`
	PlayerTimeMs   int64           `json:"playerTimeMs"`
	VideoItag      int             `json:"videoItag"`
	AudioItag      int             `json:"audioItag"`
	AudioTrackID   *string         `json:"audioTrackId"`
	PlaybackRate   float32         `json:"playbackRate"`
	BufferGoalMs   int64           `json:"bufferGoalMs"`
	BackBufferMs   int64           `json:"backBufferMs"`
	BufferedRanges []BufferedRange `json:"bufferedRanges"`
	AudioOnly      bool            `json:"audioOnly"`
}

// DefaultWindowRequest has the Kotlin defaults for the fields a client may leave out.
func DefaultWindowRequest() WindowRequest {
	return WindowRequest{PlaybackRate: 1, BufferGoalMs: 30000, BackBufferMs: 30000}
}

type WindowSegment struct {
	URL        string `json:"url"`
	StartMs    int64  `json:"startMs"`
	DurationMs int64  `json:"durationMs"`
}

type WindowTrack struct {
	Mime     string          `json:"mime"`
	InitURL  string          `json:"initUrl"`
	Segments []WindowSegment `json:"segments"`
}

type Window struct {
	Audio       WindowTrack
	Video       *WindowTrack
	DurationMs  int64
	EndOfStream bool
	StartTimeMs int64
	Ready       bool
	BlockedBy   string
	BlockedAtMs int64
}

func MediaBase(sessionID string) string { return "/api/sabr/playback/" + sessionID }

type trackWindow struct {
	track      WindowTrack
	coveredEnd int64
	atEnd      bool
	blockedBy  string
	blockedAt  int64
}

func (w trackWindow) covers(end int64) bool {
	return (len(w.track.Segments) > 0 || w.atEnd) && w.coveredEnd >= end
}

func bufferedEnd(req *WindowRequest, itag int, start int64) int64 {
	end := int64(-1)
	for _, r := range req.BufferedRanges {
		if r.Itag == itag && r.StartMs <= start && r.EndMs > start {
			end = max(end, r.EndMs)
		}
	}
	if end < 0 {
		return start
	}
	return max(end, start)
}

// BuildWindow lists, per track, the finished segments from what the client already buffered up to its buffer
// goal, and says whether at least the next second plays. Callers hold no lock.
func (s *Session) BuildWindow(req *WindowRequest) Window {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := max(0, req.PlayerTimeMs)
	gen := s.gen
	base := MediaBase(s.ID)
	genQ := "?generation=" + strconv.FormatInt(gen, 10)
	build := func(t *track) trackWindow {
		target := bufferedEnd(req, t.f.Itag, start)
		goalEnd := start + max(1, req.BufferGoalMs)
		out := trackWindow{track: WindowTrack{Mime: t.f.Mime(), InitURL: base + "/" + strconv.Itoa(t.f.Itag) + "/init" + genQ, Segments: []WindowSegment{}}}
		out.coveredEnd = target
		if t.atEnd(target) {
			out.atEnd = true
			return out
		}
		seg := t.covering(target)
		if seg == nil {
			out.blockedBy = trackName(t) + ":" + strconv.Itoa(t.f.Itag) + " pending"
			out.blockedAt = target
			return out
		}
		seq := seg.Seq
		for len(out.track.Segments) < maxSegmentsPerTrack {
			sg := t.segs[seq]
			if sg == nil || !sg.done {
				out.blockedBy = trackName(t) + ":" + strconv.Itoa(t.f.Itag) + ":" + strconv.Itoa(seq) + " pending"
				out.blockedAt = out.coveredEnd
				break
			}
			out.track.Segments = append(out.track.Segments, WindowSegment{
				URL: base + "/" + strconv.Itoa(t.f.Itag) + "/segment/" + strconv.Itoa(seq) + genQ, StartMs: sg.StartMs, DurationMs: sg.DurMs})
			out.coveredEnd = sg.EndMs()
			if t.endSeq > 0 && seq >= t.endSeq {
				out.atEnd = true
				break
			}
			if out.coveredEnd >= goalEnd {
				break
			}
			seq++
		}
		return out
	}
	readyEnd := func(t *track, want int64) int64 {
		d := t.f.DurationMs.Int()
		if t.endSeq > 0 {
			if last := t.segs[t.endSeq]; last != nil && last.done {
				d = last.EndMs()
			}
		}
		if d <= 0 {
			return want
		}
		return min(d, want)
	}
	want := start + min(max(1, req.BufferGoalMs), minReadyAheadMs)
	audio := build(s.tracks[audioTrack])
	w := Window{Audio: audio.track, DurationMs: s.durationLocked(), StartTimeMs: start}
	ready := audio.covers(readyEnd(s.tracks[audioTrack], want))
	blocked := audio
	if !req.AudioOnly {
		video := build(s.tracks[videoTrack])
		w.Video = &video.track
		ready = ready && video.covers(readyEnd(s.tracks[videoTrack], want))
		w.EndOfStream = audio.atEnd && video.atEnd
		if video.blockedBy != "" && (audio.blockedBy == "" || video.coveredEnd < audio.coveredEnd) {
			blocked = video
		}
	} else {
		w.EndOfStream = audio.atEnd
	}
	w.Ready = ready
	w.BlockedBy, w.BlockedAtMs = blocked.blockedBy, blocked.blockedAt
	if !ready && w.BlockedBy == "" {
		w.BlockedBy = "window pending"
	}
	if w.BlockedBy != "" && blocked.blockedAt > s.playerMs && blocked.blockedAt > s.demandMs {
		s.demandMs = blocked.blockedAt
	}
	return w
}

func trackName(t *track) string {
	if t.f.IsAudio() {
		return "audio"
	}
	return "video"
}
