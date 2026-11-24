package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/sabr"
)

const playbackManifestWindowMs = 30_000

// sabrTrack is one adaptation set of a SABR DASH manifest: the segments from first to last (1-based, inclusive).
type sabrTrack struct {
	f           *sabr.Format
	index       []sabr.SegTime
	first, last int
}

// writeSabrDash is SabrDashManifestBuilder: a static MPD with an explicit segment list per track, every URL under
// base with the session and any extra query.
func writeSabrDash(w http.ResponseWriter, base, query string, tracks []sabrTrack) {
	end := int64(0)
	for _, t := range tracks {
		if n := len(t.index); n > 0 {
			end = max(end, t.index[n-1].StartMs+t.index[n-1].DurMs)
		}
		end = max(end, t.f.DurationMs.Int())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<MPD xmlns=\"urn:mpeg:dash:schema:mpd:2011\"\n     profiles=\"urn:mpeg:dash:profile:full:2011\"\n     type=\"static\"\n     mediaPresentationDuration=\"PT%dS\"\n     minBufferTime=\"PT2S\">\n  <Period>\n", max(end/1000, 1))
	for _, t := range tracks {
		container, codecs := containerMime(t.f.MimeType), deref(codec(t.f.MimeType))
		if t.f.IsVideo() {
			size := ""
			if t.f.Width > 0 && t.f.Height > 0 {
				size = fmt.Sprintf(" width=\"%d\" height=\"%d\"", t.f.Width, t.f.Height)
			}
			fmt.Fprintf(&b, "    <AdaptationSet mimeType=\"%s\" startWithSAP=\"1\">\n      <Representation id=\"v\" bandwidth=\"%d\"%s codecs=\"%s\">\n",
				container, max(t.f.Bitrate, 1), size, codecs)
		} else {
			fmt.Fprintf(&b, "    <AdaptationSet mimeType=\"%s\">\n      <Representation id=\"a\" bandwidth=\"%d\" codecs=\"%s\">\n",
				container, max(t.f.Bitrate, 128_000), codecs)
		}
		prefix, q := base+"/"+strconv.Itoa(t.f.Itag), strings.ReplaceAll(query, "&", "&amp;")
		fmt.Fprintf(&b, "        <SegmentList timescale=\"1000\">\n          <Initialization sourceURL=\"%s/init%s\"/>\n          <SegmentTimeline>\n", prefix, q)
		for seq := t.first; seq <= t.last; seq++ {
			st := t.index[seq-1]
			fmt.Fprintf(&b, "            <S t=\"%d\" d=\"%d\"/>\n", st.StartMs, st.DurMs)
		}
		b.WriteString("          </SegmentTimeline>\n")
		for seq := t.first; seq <= t.last; seq++ {
			fmt.Fprintf(&b, "          <SegmentURL media=\"%s/segment/%d%s\"/>\n", prefix, seq, q)
		}
		b.WriteString("        </SegmentList>\n      </Representation>\n    </AdaptationSet>\n")
	}
	b.WriteString("  </Period>\n</MPD>")
	w.Header().Set("Content-Type", "application/dash+xml")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(b.String()))
}

// wholeSabrDash lists every segment of the session's tracks, for /sabr/manifest.
func (s *Server) wholeSabrDash(w http.ResponseWriter, r *http.Request, ss *sabr.Session, audioOnly bool) {
	formats := []*sabr.Format{ss.Video, ss.Audio}
	if audioOnly {
		formats = formats[1:]
	}
	var tracks []sabrTrack
	for _, f := range formats {
		index := ss.Index(r.Context(), f.Itag, timelineWait)
		if len(index) == 0 {
			plain(w, r, http.StatusUnprocessableEntity, "Segment index not yet available")
			return
		}
		tracks = append(tracks, sabrTrack{f: f, index: index, first: 1, last: len(index)})
	}
	writeSabrDash(w, "../"+ss.VideoID, "?session="+ss.ID, tracks)
}

func segmentAt(index []sabr.SegTime, ms int64) int {
	for i, t := range index {
		if ms < t.StartMs+t.DurMs {
			return i + 1
		}
	}
	return len(index)
}

// playbackManifest is SabrPlaybackManifestService: the segments from the player position to 30 s past what both
// tracks hold, or 202 while the start is not buffered yet.
func (s *Server) playbackManifest(w http.ResponseWriter, r *http.Request) {
	ss := s.sabrSession(w, r)
	if ss == nil {
		return
	}
	retry := func() {
		ms := int64(sabrRetryAfterMs)
		httpx.JSON(w, http.StatusAccepted, playbackResponse{SessionID: ss.ID, VideoID: ss.VideoID, VideoItag: ss.Video.Itag,
			AudioItag: ss.Audio.Itag, AudioTrackID: ss.Audio.AudioTrackID(), StartTimeMs: ss.PlayerMs(), Generation: ss.Generation(),
			Status: "preparing", RetryAfterMs: &ms})
	}
	start := max(ss.PlayerMs(), 0)
	var tracks []sabrTrack
	edge, ready := int64(-1), int64(0)
	for _, f := range []*sabr.Format{ss.Video, ss.Audio} {
		index := ss.Index(r.Context(), f.Itag, 0)
		if len(index) == 0 {
			retry()
			return
		}
		first := segmentAt(index, start)
		ready = max(ready, index[first-1].StartMs)
		bufferedEnd := ss.TrackState(f.Itag).BufferedEndMs
		if edge < 0 || bufferedEnd < edge {
			edge = bufferedEnd
		}
		tracks = append(tracks, sabrTrack{f: f, index: index, first: first})
	}
	if edge < ready {
		retry()
		return
	}
	windowEnd := max(edge, start) + playbackManifestWindowMs
	for i := range tracks {
		tracks[i].last = max(tracks[i].first, segmentAt(tracks[i].index, windowEnd))
	}
	writeSabrDash(w, "/api/sabr/playback/"+ss.ID, "?session="+ss.ID+"&generation="+strconv.FormatInt(ss.Generation(), 10), tracks)
}
