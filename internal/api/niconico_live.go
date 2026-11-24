package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/niconico"
	"typetype.dev/server/internal/youtube"
)

// niconicoLive answers a live.nicovideo.jp program as the Kotlin server does through PipePipe: a live_stream whose
// hlsUrl is a /media handle carrying the watch session's dlive_bid cookie.
func (s *Server) niconicoLive(w http.ResponseWriter, r *http.Request, raw, user string, hidden func(youtube.VideoItem) bool, focus *focusProfile) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	l, err := s.nico.Live(ctx, raw)
	switch {
	case errors.Is(err, niconico.ErrLivePaid):
		httpx.Error(w, r, http.StatusBadRequest, err.Error(), "paid_content")
		return
	case errors.Is(err, niconico.ErrLiveNotStarted):
		httpx.Error(w, r, http.StatusUnprocessableEntity, err.Error(), "live_not_started")
		return
	case errors.Is(err, niconico.ErrLiveFollowers), errors.Is(err, niconico.ErrLivePrivate):
		httpx.Error(w, r, http.StatusUnprocessableEntity, err.Error(), "content_unavailable")
		return
	case err != nil:
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not extract this live", "extraction_failed")
		return
	}
	if hidden(youtube.VideoItem{URL: raw, UploaderURL: l.SupplierURL, UploaderName: l.SupplierName}) {
		httpx.Error(w, r, http.StatusForbidden, "Channel is blocked", "content_blocked")
		return
	}
	if user != "" && !s.accessFor(r.Context(), user).allowsUploader(l.SupplierURL, l.SupplierName) {
		plain(w, r, http.StatusForbidden, "Channel is not allowed")
		return
	}
	if b := focus.playbackBlock(l.SupplierURL, nil); b != nil {
		b["channelUrl"], b["channelName"], b["channelAvatarUrl"], b["videoTitle"] = l.SupplierURL, l.SupplierName, l.AvatarURL, l.Title
		b["requestId"] = httpx.RequestID(r.Context())
		httpx.JSON(w, http.StatusForbidden, b)
		return
	}
	hls := s.mediaPath(ctx, l.HLS, nil, nil)
	if hls == "" {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Could not extract this live", "extraction_failed")
		return
	}
	resp := streamResponse{ID: raw, Title: l.Title, Description: l.Description, UploaderName: l.SupplierName, UploaderURL: l.SupplierURL,
		UploaderAvatarURL: l.AvatarURL, ThumbnailURL: l.Thumbnail, Duration: -1, ViewCount: l.WatchCount, LikeCount: -1, DislikeCount: -1,
		Uploaded: -1, UploaderSubscriberCount: -1, Visibility: "public", Tags: []string{}, StreamType: "live_stream", IsLive: true, IsLiveContent: true,
		HasLiveManifest: true, HlsURL: hls, StreamSegments: []youtube.Chapter{}, VideoStreams: []videoStream{}, AudioStreams: []audioStream{},
		VideoOnlyStreams: []videoStream{}, Subtitles: []youtube.Subtitle{}, PreviewFrames: []youtube.Frames{}, SponsorBlockSegments: []sponsorSegment{},
		RelatedStreams: []youtube.VideoItem{}, Collections: []any{}, Parts: []any{}, EndScreenElements: []any{}}
	httpx.JSON(w, http.StatusOK, resp)
}
