package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/sabr"
)

type sabrFormatJSON struct {
	Itag             int     `json:"itag"`
	MimeType         string  `json:"mimeType"`
	Bitrate          int64   `json:"bitrate"`
	ApproxDurationMs int64   `json:"approxDurationMs"`
	QualityLabel     *string `json:"qualityLabel"`
	AudioTrackID     *string `json:"audioTrackId"`
	AudioTrackName   *string `json:"audioTrackName"`
	Init             string  `json:"init"`
}

func sabrFormatOf(f *sabr.Format) sabrFormatJSON {
	out := sabrFormatJSON{Itag: f.Itag, MimeType: f.MimeType, Bitrate: f.Bitrate, ApproxDurationMs: f.DurationMs.Int(),
		AudioTrackID: f.AudioTrackID(), Init: "/sabr/{videoId}/" + strconv.Itoa(f.Itag) + "/init?session={session}"}
	if f.QualityLabel != "" {
		out.QualityLabel = &f.QualityLabel
	}
	if f.AudioTrack != nil {
		out.AudioTrackName = &f.AudioTrack.DisplayName
	}
	return out
}

func (s *Server) sabrSessionRoutes(rt *Router) {
	rt.Handle("GET", "/sabr/session/{videoId}", s.sabrDescriptor)
	rt.Handle("GET", "/sabr/session/{videoId}/state", s.sabrSessionState)
	rt.Handle("POST", "/sabr/session/{videoId}/state", s.sabrSessionState)
}

// sabrDescriptor is SabrSessionDescriptorHandler: it opens a session for the legacy segment routes, waits until it
// can play from the start time, and describes its formats and endpoints.
func (s *Server) sabrDescriptor(w http.ResponseWriter, r *http.Request) {
	videoID, q := r.PathValue("videoId"), r.URL.Query()
	if _, fail := s.streamFor(r, "https://www.youtube.com/watch?v="+videoID); fail != nil {
		relay(w, fail)
		return
	}
	start, err := strconv.ParseInt(q.Get("playerTimeMs"), 10, 64)
	if err != nil {
		start, _ = strconv.ParseInt(q.Get("startTimeMs"), 10, 64)
	}
	start = max(start, 0)
	ctx, cancel := context.WithTimeout(r.Context(), sabrInfoTimeout)
	info, err := s.Sabr.Token().Info(ctx, videoID)
	cancel()
	if err != nil {
		plain(w, r, http.StatusUnprocessableEntity, "SABR probe failed")
		return
	}
	aitag, _ := strconv.Atoi(q.Get("audioItag"))
	audio := info.SelectAudio(aitag, q.Get("audioTrackId"))
	if audio == nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "No SABR audio for this video", "no_playable_streams")
		return
	}
	vitag, _ := strconv.Atoi(q.Get("videoItag"))
	video := info.SelectVideo(vitag)
	if video == nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "No SABR video for this video", "no_playable_streams")
		return
	}
	owner := s.optionalUserID(r)
	if owner == "" {
		owner = videoID
	}
	ss := s.Sabr.Create(info, videoID, owner, video, audio, start, false, 0)
	s.awaitInit(r.Context(), ss)
	if !s.awaitReady(r.Context(), ss, start, false) {
		plain(w, r, http.StatusUnprocessableEntity, "SABR preflight failed")
		return
	}
	init := func(f *sabr.Format) string {
		return "/sabr/" + videoID + "/" + strconv.Itoa(f.Itag) + "/init?session=" + ss.ID
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"videoId": videoID, "session": ss.ID, "transport": "http-segments", "protocol": "typetype-sabr-http-v1",
		"startTimeMs": start, "durationMs": max(audio.DurationMs.Int(), video.DurationMs.Int()),
		"audio": sabrFormatOf(audio), "video": sabrFormatOf(video),
		"endpoints": map[string]string{
			"hls":       "/sabr/manifest/" + videoID + "?format=hls&session=" + ss.ID,
			"dash":      "/sabr/manifest/" + videoID + "?session=" + ss.ID,
			"audioInit": init(audio), "videoInit": init(video),
		},
	})
}

// sabrSessionState is SabrSessionStateHandler: POST moves the player, switches video off or on and seeks to a
// segment; both answer the session state.
func (s *Server) sabrSessionState(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("session")
	if token == "" {
		plain(w, r, http.StatusBadRequest, "Missing SABR session")
		return
	}
	ss := s.Sabr.Lookup(token)
	if ss == nil || ss.VideoID != r.PathValue("videoId") {
		plain(w, r, http.StatusNotFound, "No active SABR session for this request")
		return
	}
	if r.Method == http.MethodPost {
		var body struct {
			AudioActive  *bool  `json:"audioActive"`
			VideoActive  *bool  `json:"videoActive"`
			PlayerTimeMs *int64 `json:"playerTimeMs"`
			SeekItag     *int   `json:"seekItag"`
			SeekSequence *int   `json:"seekSequence"`
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		var probe map[string]json.RawMessage
		if json.Unmarshal(raw, &probe) != nil || probe == nil || json.Unmarshal(raw, &body) != nil {
			plain(w, r, http.StatusBadRequest, "Invalid SABR state body")
			return
		}
		audioOnly := ss.AudioOnly
		if body.VideoActive != nil {
			audioOnly = !*body.VideoActive
		}
		if body.PlayerTimeMs != nil || audioOnly != ss.AudioOnly {
			pos := ss.PlayerMs()
			if body.PlayerTimeMs != nil {
				pos = *body.PlayerTimeMs
			}
			ss.SetPosition(ss.Generation(), pos, audioOnly)
		}
		if body.SeekItag != nil && body.SeekSequence != nil && *body.SeekSequence > 0 {
			ss.Pull(*body.SeekItag, *body.SeekSequence)
		}
	}
	st := ss.Stats()
	track := func(f *sabr.Format, active bool) map[string]any {
		t := ss.TrackState(f.Itag)
		return map[string]any{"itag": f.Itag, "active": active, "maxSegment": t.MaxSegment, "endSegment": t.EndSegment,
			"bufferedEndMs": t.BufferedEndMs}
	}
	audio, video := ss.TrackState(ss.Audio.Itag), ss.TrackState(ss.Video.Itag)
	minEnd := audio.BufferedEndMs
	if !ss.AudioOnly {
		minEnd = min(minEnd, video.BufferedEndMs)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"videoId": ss.VideoID, "session": ss.ID, "requestNumber": ss.Requests(), "complete": ss.Complete(), "live": false,
		"cachedBytes": st.CachedBytes, "playerTimeMs": st.PlayerMs, "readerHeadMs": st.ReaderHead, "readerTailMs": st.ReaderTail,
		"minBufferedEndMs": minEnd, "pendingSeek": st.Demand > 0, "diagnosticTrace": []string{},
		"tracks": map[string]any{"audio": track(ss.Audio, true), "video": track(ss.Video, !ss.AudioOnly)},
	})
}
