package api

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/sabr"
)

// The TypeType SABR download stream, read by TypeType-Downloader: a magic line, then frames of a 17-byte header
// (type, itag, sequence as int32, length as int64, big endian) and their bytes, then a complete frame.
var sabrDownloadMagic = []byte("TTSABR1\n")

const (
	frameInit     = 1
	frameMedia    = 2
	frameComplete = 3
)

func writeFrame(w io.Writer, kind byte, itag, seq int, data []byte) error {
	var h [17]byte
	h[0] = kind
	binary.BigEndian.PutUint32(h[1:5], uint32(int32(itag)))
	binary.BigEndian.PutUint32(h[5:9], uint32(int32(seq)))
	binary.BigEndian.PutUint64(h[9:17], uint64(len(data)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

type dlTrack struct {
	f               *sabr.Format
	next, end, size int
	times           []sabr.SegTime
}

// boundary is SabrDownloadRange.boundarySequence: the first sequence of part p of parts.
func (t *dlTrack) boundary(p, parts int) (int, error) {
	if parts == 1 || p == 0 {
		return 1, nil
	}
	if t.size >= parts {
		return 1 + t.size*p/parts, nil
	}
	dur := t.f.DurationMs.Int()
	if dur <= 0 {
		return 0, errors.New("SABR duration is required for multipart downloads")
	}
	at := dur * int64(p) / int64(parts)
	for i, st := range t.times {
		if st.StartMs >= at {
			return i + 1, nil
		}
	}
	return t.size + 1, nil
}

// downloadSabr is SabrDownloadStreamer.stream over a session the request owns: the initialization of each track,
// then the part's media segments of both tracks in play order. The pump follows the segment asked for, so the
// session keeps the playback lead and back buffer and never holds the whole file.
// emit receives each frame; it returns an error to stop the download.
type emitFunc func(kind byte, itag, seq int, data []byte) error

func downloadSabr(ctx context.Context, ss *sabr.Session, formats []*sabr.Format, part, parts int, emit emitFunc) error {
	tracks := make([]*dlTrack, 0, len(formats))
	for _, f := range formats {
		data, ok := ss.Init(ctx, f.Itag, 20*time.Second)
		if !ok {
			return errors.New("Missing SABR initialization for itag " + strconv.Itoa(f.Itag))
		}
		if err := emit(frameInit, f.Itag, 0, data); err != nil {
			return err
		}
		times := ss.Index(ctx, f.Itag, 20*time.Second)
		if len(times) == 0 {
			return errors.New("SABR download could not read the segment count")
		}
		t := &dlTrack{f: f, size: len(times), times: times}
		var err error
		if t.next, err = t.boundary(part, parts); err != nil {
			return err
		}
		t.end = t.size + 1
		if part+1 < parts {
			if t.end, err = t.boundary(part+1, parts); err != nil {
				return err
			}
		}
		tracks = append(tracks, t)
	}
	for {
		var t *dlTrack
		for _, c := range tracks {
			if c.next < c.end && (t == nil || float64(c.next-1)/float64(c.size) < float64(t.next-1)/float64(t.size)) {
				t = c
			}
		}
		if t == nil {
			break
		}
		ss.Pull(t.f.Itag, t.next)
		seg, ok := ss.Media(ctx, t.f.Itag, t.next, 30*time.Second)
		if !ok {
			if kind, msg := ss.Failure(); kind != "" {
				return errors.New("SABR download failed: " + msg)
			}
			return errors.New("SABR download stalled without receiving media")
		}
		err := emit(frameMedia, t.f.Itag, t.next, seg.Data())
		seg.Release()
		if err != nil {
			return err
		}
		t.next++
	}
	return emit(frameComplete, 0, 0, nil)
}

// sabrDownload is /sabr/download/{videoId}: the manifest's access rules, a session of its own started at the part's
// time, and the stream above. A failure after the first byte ends the stream without its complete frame, which the
// downloader treats as a failed part and asks again.
func (s *Server) sabrDownload(w http.ResponseWriter, r *http.Request) {
	videoID, q := r.PathValue("videoId"), r.URL.Query()
	parts, part := 1, 0
	if v, err := strconv.Atoi(q.Get("parts")); err == nil {
		parts = v
	}
	if v, err := strconv.Atoi(q.Get("part")); err == nil {
		part = v
	}
	switch {
	case parts < 1 || parts > 12:
		plain(w, r, http.StatusBadRequest, "parts must be between 1 and 12")
		return
	case part < 0 || part >= parts:
		plain(w, r, http.StatusBadRequest, "part must be between 0 and parts - 1")
		return
	case q.Get("session") != "":
		plain(w, r, http.StatusBadRequest, "Download streams require a new SABR session")
		return
	}
	audioOnly := strings.EqualFold(q.Get("audioOnly"), "true")
	info, audio, video, owner, ok := s.sabrSelect(w, r, videoID)
	if !ok {
		return
	}
	var start int64
	if part > 0 {
		dur := audio.DurationMs.Int()
		if !audioOnly {
			dur = max(dur, video.DurationMs.Int())
		}
		start = max(dur, 0) * int64(part) / int64(parts)
	}
	ss := s.Sabr.Create(info, videoID, owner, video, audio, start, audioOnly, 0)
	defer s.Sabr.Release(ss)
	formats := []*sabr.Format{audio}
	if !audioOnly {
		formats = append(formats, video)
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "application/vnd.typetype.sabr-download")
	w.WriteHeader(http.StatusOK)
	w.Write(sabrDownloadMagic)
	rc := http.NewResponseController(w)
	emit := func(kind byte, itag, seq int, data []byte) error {
		if err := writeFrame(w, kind, itag, seq, data); err != nil {
			return err
		}
		return rc.Flush()
	}
	if err := downloadSabr(r.Context(), ss, formats, part, parts, emit); err != nil && r.Context().Err() == nil {
		s.Log.Warn("sabr_download_failed", "videoId", videoID, "part", strconv.Itoa(part+1)+"/"+strconv.Itoa(parts), "reason", err.Error())
	}
}
