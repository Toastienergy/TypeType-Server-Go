package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"typetype.dev/server/internal/bilibili"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

// bilibiliLive is the live room page: the HLS master playlist and the single progressive stream (the FLV, or the
// master playlist when the room has no FLV) behind media handles, as ProviderMediaHandleService.materialize.
func (s *Server) bilibiliLive(ctx context.Context, w http.ResponseWriter, r *http.Request, raw, user string, hidden func(youtube.VideoItem) bool, focus *focusProfile) {
	l, err := s.bili.Live(ctx, raw)
	switch {
	case errors.Is(err, bilibili.ErrLiveNotStarted):
		httpx.Error(w, r, http.StatusUnprocessableEntity, err.Error(), "live_not_started")
		return
	case errors.Is(err, bilibili.ErrRiskControl):
		w.Header().Set("Retry-After", "30")
		httpx.Error(w, r, http.StatusServiceUnavailable, "Bilibili risk control blocked this server", "provider_access_blocked")
		return
	case err != nil:
		httpx.Error(w, r, http.StatusUnprocessableEntity, err.Error(), "content_unavailable")
		return
	}
	resp := streamResponse{ID: strconv.FormatInt(l.RoomID, 10), Title: l.Title, Description: l.Description, UploaderName: l.Uploader,
		UploaderURL: spaceURL(l.UID), UploaderAvatarURL: l.Face, ThumbnailURL: l.Cover, Duration: -1, ViewCount: l.Online, LikeCount: -1,
		DislikeCount: -1, Uploaded: -1, UploaderSubscriberCount: -1, Visibility: "public", Tags: []string{}, StreamType: "live_stream",
		IsLive: true, IsLiveContent: true, StreamSegments: []youtube.Chapter{}, VideoStreams: []videoStream{}, AudioStreams: []audioStream{},
		VideoOnlyStreams: []videoStream{}, Subtitles: []youtube.Subtitle{}, PreviewFrames: []youtube.Frames{}, SponsorBlockSegments: []sponsorSegment{},
		RelatedStreams: []youtube.VideoItem{}, Collections: []any{}, Parts: []any{}, EndScreenElements: []any{}}
	if hidden(youtube.VideoItem{URL: raw, UploaderURL: resp.UploaderURL, UploaderName: resp.UploaderName}) {
		httpx.Error(w, r, http.StatusForbidden, "Channel is blocked", "content_blocked")
		return
	}
	if !s.accessFor(ctx, user).allowsUploader(resp.UploaderURL, resp.UploaderName) {
		plain(w, r, http.StatusForbidden, "Channel is not allowed")
		return
	}
	if b := focus.playbackBlock(resp.UploaderURL, nil); b != nil {
		b["channelUrl"], b["channelName"], b["channelAvatarUrl"], b["videoTitle"] = resp.UploaderURL, resp.UploaderName, resp.UploaderAvatarURL, resp.Title
		b["requestId"] = httpx.RequestID(r.Context())
		httpx.JSON(w, http.StatusForbidden, b)
		return
	}
	if l.HLS != "" {
		resp.HlsURL = s.mediaPath(ctx, l.HLS, nil, nil)
	}
	if single := l.FLV; single != "" || l.HLS != "" {
		if single == "" {
			single = l.HLS
		}
		if p := s.mediaPath(ctx, single, nil, nil); p != "" {
			resp.VideoStreams = append(resp.VideoStreams, videoStream{URL: p, Resolution: "720p", Itag: -1, DeliveryMethod: "progressive"})
		}
	}
	httpx.JSON(w, http.StatusOK, resp)
}
