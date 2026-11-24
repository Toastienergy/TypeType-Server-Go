package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/sabr"
)

const (
	legacyInitWait    = 20 * time.Second
	legacySegmentWait = 60 * time.Second
	timelineWait      = 15 * time.Second
)

var tokenVideoRe = regexp.MustCompile(`(?:[?&]v=|/shorts/|youtu\.be/)([A-Za-z0-9_-]{6,})`)

func (s *Server) sabrManifestRoutes(rt *Router) {
	rt.Handle("GET", "/sabr/manifest/{videoId}", s.sabrManifest)
	rt.Handle("GET", "/sabr/download/{videoId}", s.sabrDownload)
	rt.Handle("GET", "/sabr/playback/{sessionId}/manifest", s.playbackManifest)
	rt.Handle("GET", "/sabr/{videoId}/{itag}/init", s.legacyMedia(true))
	rt.Handle("GET", "/sabr/{videoId}/{itag}/segment/{seq}", s.legacyMedia(false))
}

// sabrManifest is SabrManifestHandler for HLS: an audio-only media playlist, or a master pointing at the audio and
// video playlists of the same session. Its segments are the legacy /sabr/{videoId}/{itag} routes, which a plain
// HLS player fetches in order and which move the session along as they are asked for.
func (s *Server) sabrManifest(w http.ResponseWriter, r *http.Request) {
	videoID, q := r.PathValue("videoId"), r.URL.Query()
	hls := strings.EqualFold(q.Get("format"), "hls")
	audioOnly := strings.EqualFold(q.Get("audioOnly"), "true")
	if session := q.Get("session"); session != "" {
		ss := s.Sabr.Lookup(session)
		if ss == nil || ss.VideoID != videoID {
			plain(w, r, http.StatusNotFound, "No active SABR session for this request")
			return
		}
		pl := q.Get("playlist")
		switch {
		case !hls:
			s.wholeSabrDash(w, r, ss, audioOnly)
			return
		case pl == "" && audioOnly:
			s.writeMediaPlaylist(w, r, ss, ss.Audio)
			return
		case pl == "":
			writeMaster(w, ss)
			return
		}
		f := map[string]*sabr.Format{"audio": ss.Audio, "video": ss.Video}[pl]
		if f == nil {
			plain(w, r, http.StatusBadRequest, "Invalid HLS playlist")
			return
		}
		s.writeMediaPlaylist(w, r, ss, f)
		return
	}
	info, audio, video, owner, ok := s.sabrSelect(w, r, videoID)
	if !ok {
		return
	}
	start, _ := strconv.ParseInt(q.Get("startTimeMs"), 10, 64)
	ss := s.Sabr.Create(info, videoID, owner, video, audio, max(start, 0), audioOnly, 0)
	if !hls {
		s.wholeSabrDash(w, r, ss, audioOnly)
		return
	}
	if audioOnly {
		s.writeMediaPlaylist(w, r, ss, audio)
		return
	}
	writeMaster(w, ss)
}

func writeMaster(w http.ResponseWriter, ss *sabr.Session) {
	audio, video := ss.Audio, ss.Video
	base := "?format=hls&session=" + ss.ID
	codecs := strings.Trim(strings.Join([]string{deref(codec(video.MimeType)), deref(codec(audio.MimeType))}, ","), ",")
	res := ""
	if video.Width > 0 && video.Height > 0 {
		res = fmt.Sprintf("RESOLUTION=%dx%d,", video.Width, video.Height)
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"audio\",DEFAULT=YES,AUTOSELECT=YES,URI=\"%s&playlist=audio\"\n", base)
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,%sCODECS=\"%s\",AUDIO=\"audio\"\n%s&playlist=video\n", max(video.Bitrate+audio.Bitrate, 1), res, codecs, base)
	writeHLS(w, b.String())
}

func writeHLS(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", hlsMime)
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(body))
}

// writeMediaPlaylist is SabrHlsManifestBuilder's VOD playlist: every segment of the format, with durations from the
// index where known and the average elsewhere, relative to /sabr/manifest/{videoId}.
func (s *Server) writeMediaPlaylist(w http.ResponseWriter, r *http.Request, ss *sabr.Session, f *sabr.Format) {
	index := ss.Index(r.Context(), f.Itag, timelineWait)
	if len(index) == 0 {
		plain(w, r, http.StatusUnprocessableEntity, "Segment index not yet available")
		return
	}
	target := int64(1)
	for _, t := range index {
		target = max(target, (t.DurMs+999)/1000)
	}
	query := "?session=" + ss.ID
	prefix := "../" + ss.VideoID + "/" + strconv.Itoa(f.Itag)
	var b strings.Builder
	b.Grow(64 * len(index))
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-PLAYLIST-TYPE:VOD\n", target)
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s/init%s\"\n", prefix, query)
	for i, t := range index {
		fmt.Fprintf(&b, "#EXTINF:%d.%03d,\n%s/segment/%d%s\n", t.DurMs/1000, t.DurMs%1000, prefix, i+1, query)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	writeHLS(w, b.String())
}

// legacyMedia is SabrSegmentHandler: the session comes from ?session=, the request waits up to 20 s for an init
// and 60 s for a segment, then answers 404, which an HLS player retries.
func (s *Server) legacyMedia(isInit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		itag, err := strconv.Atoi(r.PathValue("itag"))
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Invalid path")
			return
		}
		ss := s.Sabr.Lookup(r.URL.Query().Get("session"))
		if ss == nil || ss.VideoID != r.PathValue("videoId") || itag != ss.Audio.Itag && itag != ss.Video.Itag {
			plain(w, r, http.StatusNotFound, "No active SABR session for this request")
			return
		}
		f := ss.Video
		if itag == ss.Audio.Itag {
			f = ss.Audio
		}
		if isInit {
			data, ok := ss.Init(r.Context(), itag, legacyInitWait)
			if !ok {
				plain(w, r, http.StatusNotFound, "Segment not available")
				return
			}
			writeMedia(w, r, containerMime(f.MimeType), data)
			return
		}
		seq, err := strconv.Atoi(r.PathValue("seq"))
		if err != nil || seq < 1 {
			plain(w, r, http.StatusBadRequest, "Invalid seq")
			return
		}
		ss.Pull(itag, seq)
		seg, ok := ss.Media(r.Context(), itag, seq, legacySegmentWait)
		if !ok {
			plain(w, r, http.StatusNotFound, "Segment not available")
			return
		}
		writeMedia(w, r, containerMime(f.MimeType), seg.Data())
	}
}

// sabrSelect is the shared front of SabrManifestHandler: an audio-only token or the stream guard, the SABR info, and
// the audio and video formats the query or the token asks for. It answers the error itself when ok is false.
func (s *Server) sabrSelect(w http.ResponseWriter, r *http.Request, videoID string) (info *sabr.Info, audio, video *sabr.Format, owner string, ok bool) {
	q := r.URL.Query()
	var tok audioToken
	owner = videoID
	if raw := q.Get("audioToken"); raw != "" {
		t, msg := s.verifyAudioToken(raw)
		if msg == "" {
			if m := tokenVideoRe.FindStringSubmatch(t.VideoURL); m == nil || m[1] != videoID {
				msg = "Invalid audio-only token"
			}
		}
		if msg != "" {
			plain(w, r, http.StatusUnauthorized, msg)
			return nil, nil, nil, "", false
		}
		tok = t
		if t.UserID != "" {
			owner = t.UserID
		}
	} else {
		if _, fail := s.streamFor(r, "https://www.youtube.com/watch?v="+videoID); fail != nil {
			relay(w, fail)
			return nil, nil, nil, "", false
		}
		if u := s.optionalUserID(r); u != "" {
			owner = u
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), sabrInfoTimeout)
	info, err := s.Sabr.Token().Info(ctx, videoID)
	cancel()
	if err != nil {
		plain(w, r, http.StatusUnprocessableEntity, "SABR probe failed")
		return nil, nil, nil, "", false
	}
	itag, _ := strconv.Atoi(q.Get("audioItag"))
	track := q.Get("audioTrackId")
	if tok.Itag != 0 {
		itag, track = tok.Itag, tok.TrackID
	}
	audio = info.SelectAudio(itag, track)
	if audio == nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "No SABR audio for this video", "no_playable_streams")
		return nil, nil, nil, "", false
	}
	vitag, _ := strconv.Atoi(q.Get("videoItag"))
	video = info.SelectVideo(vitag)
	if video == nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "No SABR video for this video", "no_playable_streams")
		return nil, nil, nil, "", false
	}
	return info, audio, video, owner, true
}
