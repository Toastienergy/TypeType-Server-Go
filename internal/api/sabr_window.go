package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/sabr"
)

type windowReady struct {
	SessionID    string            `json:"sessionId"`
	Generation   int64             `json:"generation"`
	Ready        bool              `json:"ready"`
	RetryAfterMs *int64            `json:"retryAfterMs"`
	DurationMs   int64             `json:"durationMs"`
	EndOfStream  bool              `json:"endOfStream"`
	Audio        sabr.WindowTrack  `json:"audio"`
	Video        *sabr.WindowTrack `json:"video"`
	StartTimeMs  int64             `json:"startTimeMs"`
	Live         any               `json:"live"`
}

type windowPending struct {
	SessionID            string  `json:"sessionId"`
	Generation           int64   `json:"generation"`
	Ready                bool    `json:"ready"`
	RetryAfterMs         *int64  `json:"retryAfterMs"`
	Status               string  `json:"status"`
	SegmentsURL          *string `json:"segmentsUrl,omitempty"`
	StateURL             *string `json:"stateUrl,omitempty"`
	BlockedBy            *string `json:"blockedBy"`
	PlayerTimeMs         int64   `json:"playerTimeMs"`
	ReaderHeadMs         int64   `json:"readerHeadMs"`
	ReaderTailMs         int64   `json:"readerTailMs"`
	BufferedEdgeMs       int64   `json:"bufferedEdgeMs"`
	PendingRefetch       *string `json:"pendingRefetch"`
	PendingForwardSeek   *string `json:"pendingForwardSeek"`
	PendingSegmentDemand *string `json:"pendingSegmentDemand"`
	TerminalError        *string `json:"terminalError"`
	RecoveryAction       *string `json:"recoveryAction"`
	RetryVideoItags      []int   `json:"retryVideoItags"`
	Live                 any     `json:"live"`
}

func strp(s string) *string { return &s }

func readWindowRequest(w http.ResponseWriter, r *http.Request, what string) (sabr.WindowRequest, bool) {
	req := sabr.DefaultWindowRequest()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var probe map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(body, &probe)
	}
	if err == nil {
		for _, k := range []string{"generation", "playerTimeMs", "videoItag", "audioItag"} {
			if _, ok := probe[k]; !ok {
				err = io.ErrUnexpectedEOF
			}
		}
	}
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		plain(w, r, http.StatusBadRequest, "Invalid "+what+" request")
		return req, false
	}
	return req, true
}

func formatsMatch(ss *sabr.Session, req *sabr.WindowRequest) bool {
	track := ss.Audio.AudioTrackID()
	sameTrack := track == nil && req.AudioTrackID == nil || track != nil && req.AudioTrackID != nil && *track == *req.AudioTrackID
	return req.VideoItag == ss.Video.Itag && req.AudioItag == ss.Audio.Itag && sameTrack
}

func validRate(r float32) bool { return r >= 0.25 && r <= 4 }

func (s *Server) sabrWindow(action string) http.HandlerFunc {
	what := action
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := readWindowRequest(w, r, what)
		if !ok {
			return
		}
		ss := s.sabrSession(w, r)
		if ss == nil {
			return
		}
		label := "Window"
		if action == "position" {
			label = "Position"
		}
		if !formatsMatch(ss, &req) {
			plain(w, r, http.StatusBadRequest, label+" formats do not match session")
			return
		}
		if req.Generation != ss.Generation() {
			plain(w, r, http.StatusBadRequest, "Invalid generation")
			return
		}
		if !validRate(req.PlaybackRate) {
			plain(w, r, http.StatusBadRequest, "Invalid playback rate")
			return
		}
		if action != "segments" {
			ss.SetPosition(req.Generation, req.PlayerTimeMs, req.AudioOnly)
		}
		win := s.awaitWindow(r, ss, &req)
		st := ss.Stats()
		ready := win.Ready && st.FailKind == ""
		if !ready {
			s.Log.Info("sabr_window_pending", "videoId", ss.VideoID, "action", action, "playerMs", req.PlayerTimeMs,
				"generation", req.Generation, "fail", st.FailKind)
		}
		if action == "prefetch" {
			resp := s.pending(ss, &req, win, st, ready)
			base := sabr.MediaBase(ss.ID)
			resp.SegmentsURL, resp.StateURL = strp(base+"/segments"), strp(base+"/state")
			status := http.StatusAccepted
			if ready {
				status, resp.RetryAfterMs, resp.BlockedBy = http.StatusOK, nil, nil
			}
			httpx.JSON(w, status, resp)
			return
		}
		if ready {
			httpx.JSON(w, http.StatusOK, windowReady{SessionID: ss.ID, Generation: st.Generation, Ready: true,
				DurationMs: win.DurationMs, EndOfStream: win.EndOfStream, Audio: win.Audio, Video: win.Video,
				StartTimeMs: win.StartTimeMs})
			return
		}
		resp := s.pending(ss, &req, win, st, false)
		resp.RetryVideoItags = []int{}
		httpx.JSON(w, http.StatusAccepted, resp)
	}
}

func (s *Server) awaitWindow(r *http.Request, ss *sabr.Session, req *sabr.WindowRequest) sabr.Window {
	deadline := time.Now().Add(windowWait)
	for {
		win := ss.BuildWindow(req)
		left := time.Until(deadline)
		if win.Ready || left <= 0 || r.Context().Err() != nil {
			return win
		}
		if kind, _ := ss.Failure(); kind != "" {
			return win
		}
		ss.Wait(r.Context(), left)
	}
}

func (s *Server) pending(ss *sabr.Session, req *sabr.WindowRequest, win sabr.Window, st sabr.Stats, ready bool) windowPending {
	retry := int64(sabrRetryAfterMs)
	resp := windowPending{SessionID: ss.ID, Generation: st.Generation, Ready: ready, RetryAfterMs: &retry,
		Status: "preparing", PlayerTimeMs: max(0, req.PlayerTimeMs), ReaderHeadMs: st.ReaderHead, ReaderTailMs: st.ReaderTail,
		BufferedEdgeMs: st.ReaderHead, RetryVideoItags: []int{}}
	if ready {
		resp.Status = "playing"
	}
	if win.BlockedBy != "" {
		resp.BlockedBy = strp(win.BlockedBy)
	} else {
		resp.BlockedBy = strp("window pending")
	}
	if st.FailKind != "" {
		resp.Status = "failed"
		resp.TerminalError = strp(st.FailMsg)
		resp.RecoveryAction = strp("retry_fresh_session")
	}
	return resp
}

func (s *Server) sabrPosition(w http.ResponseWriter, r *http.Request) {
	req, ok := readWindowRequest(w, r, "position")
	if !ok {
		return
	}
	ss := s.sabrSession(w, r)
	if ss == nil {
		return
	}
	if !formatsMatch(ss, &req) {
		plain(w, r, http.StatusBadRequest, "Position formats do not match session")
		return
	}
	if !validRate(req.PlaybackRate) {
		plain(w, r, http.StatusBadRequest, "Invalid playback rate")
		return
	}
	ss.SetPosition(req.Generation, req.PlayerTimeMs, req.AudioOnly)
	st := ss.Stats()
	httpx.JSON(w, http.StatusOK, map[string]any{"sessionId": ss.ID, "generation": st.Generation, "playerTimeMs": st.PlayerMs,
		"readerHeadMs": st.ReaderHead, "readerTailMs": st.ReaderTail, "bufferedEdgeMs": st.ReaderHead, "live": nil})
}

func (s *Server) sabrState(w http.ResponseWriter, r *http.Request) {
	ss := s.sabrSession(w, r)
	if ss == nil {
		return
	}
	st := ss.Stats()
	state := "playing"
	var terminal *string
	if st.FailKind != "" {
		state, terminal = "failed", strp(st.FailMsg)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"sessionId": ss.ID, "videoId": ss.VideoID, "state": state,
		"videoItag": ss.Video.Itag, "audioItag": ss.Audio.Itag, "audioTrackId": ss.Audio.AudioTrackID(), "generation": st.Generation,
		"requestedSeekTimeMs": nil, "playerTimeMs": st.PlayerMs, "readerHeadMs": st.ReaderHead, "readerTailMs": st.ReaderTail,
		"bufferedEdgeMs": st.ReaderHead, "cachedBytes": st.CachedBytes, "requestNumber": 0, "pendingRefetch": nil,
		"pendingForwardSeek": nil, "pendingSegmentDemand": nil, "terminalError": terminal, "diagnosticTrace": nil, "live": nil})
}

// containerMime drops the codecs: "video/webm; codecs=\"vp9\"" becomes "video/webm".
func containerMime(m string) string {
	if i := strings.IndexByte(m, ';'); i >= 0 {
		return strings.TrimSpace(m[:i])
	}
	return m
}

func (s *Server) sabrMedia(isInit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ss := s.sabrSession(w, r)
		if ss == nil {
			return
		}
		itag, err := strconv.Atoi(r.PathValue("itag"))
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Invalid itag")
			return
		}
		seq := 0
		if !isInit {
			if seq, err = strconv.Atoi(r.PathValue("seq")); err != nil {
				plain(w, r, http.StatusBadRequest, "Invalid seq")
				return
			}
		}
		var f *sabr.Format
		switch itag {
		case ss.Audio.Itag:
			f = ss.Audio
		case ss.Video.Itag:
			f = ss.Video
		default:
			plain(w, r, http.StatusNotFound, "No active SABR track for this request")
			return
		}
		active := ss.Generation()
		gen := active
		if v := r.URL.Query().Get("generation"); v != "" {
			if g, err := strconv.ParseInt(v, 10, 64); err == nil {
				gen = g
			}
		}
		if gen > active {
			plain(w, r, http.StatusBadRequest, "Invalid generation")
			return
		}
		wait := segmentWait
		if gen < active {
			wait = 0
		}
		var data []byte
		var ok bool
		if isInit {
			data, ok = ss.Init(r.Context(), itag, max(wait, time.Millisecond))
		} else {
			var seg *sabr.Segment
			if seg, ok = ss.Media(r.Context(), itag, seq, max(wait, time.Millisecond)); ok {
				defer seg.Release()
				data = seg.Data()
			}
		}
		if ok {
			writeMedia(w, r, containerMime(f.MimeType), data)
			return
		}
		if gen < active {
			plain(w, r, http.StatusConflict, "Stale SABR playback generation")
			return
		}
		if kind, _ := ss.Failure(); kind != "" {
			sabrFailure(w, r, kind)
			return
		}
		retry := int64(sabrRetryAfterMs)
		httpx.JSON(w, http.StatusAccepted, playbackResponse{SessionID: ss.ID, VideoID: ss.VideoID, VideoItag: ss.Video.Itag,
			AudioItag: ss.Audio.Itag, AudioTrackID: ss.Audio.AudioTrackID(), StartTimeMs: ss.PlayerMs(), Generation: active,
			Status: "preparing", RetryAfterMs: &retry})
	}
}

// writeMedia serves cached bytes as they are, with single byte ranges, never buffered or copied.
// Shared one-value header slices for the segment route, the most frequent request during playback: Header.Set
// allocates a slice per call, and nothing writes into these.
var (
	noStoreValue    = []string{"no-store"}
	byteRangesValue = []string{"bytes"}
	mediaTypes      = map[string][]string{"video/mp4": {"video/mp4"}, "audio/mp4": {"audio/mp4"},
		"video/webm": {"video/webm"}, "audio/webm": {"audio/webm"}}
)

func writeMedia(w http.ResponseWriter, r *http.Request, mime string, data []byte) {
	h := w.Header()
	h["Cache-Control"] = noStoreValue
	h["Accept-Ranges"] = byteRangesValue
	if v, ok := mediaTypes[mime]; ok {
		h["Content-Type"] = v
	} else {
		h.Set("Content-Type", mime)
	}
	total := int64(len(data))
	if rg := r.Header.Get("Range"); rg != "" {
		first, last, ok := parseRange(rg, total)
		if !ok {
			h.Set("Content-Range", "bytes */"+strconv.FormatInt(total, 10))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		h.Set("Content-Range", "bytes "+strconv.FormatInt(first, 10)+"-"+strconv.FormatInt(last, 10)+"/"+strconv.FormatInt(total, 10))
		h.Set("Content-Length", strconv.FormatInt(last-first+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[first : last+1])
		return
	}
	h.Set("Content-Length", strconv.FormatInt(total, 10))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func parseRange(v string, total int64) (int64, int64, bool) {
	spec, ok := strings.CutPrefix(v, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	a, b, _ := strings.Cut(spec, "-")
	if a == "" {
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		return max(0, total-n), total - 1, total > 0
	}
	first, err := strconv.ParseInt(a, 10, 64)
	if err != nil || first >= total {
		return 0, 0, false
	}
	last := total - 1
	if b != "" {
		if l, err := strconv.ParseInt(b, 10, 64); err == nil && l < last {
			last = l
		}
	}
	return first, last, first <= last
}
