package api

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/sabr"
	"typetype.dev/server/internal/youtube"
)

type videoStream struct {
	URL            string  `json:"url"`
	MimeType       string  `json:"mimeType"`
	Format         string  `json:"format"`
	Resolution     string  `json:"resolution"`
	Bitrate        *int64  `json:"bitrate"`
	Codec          *string `json:"codec"`
	IsVideoOnly    bool    `json:"isVideoOnly"`
	Itag           int     `json:"itag"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	Fps            int     `json:"fps"`
	ContentLength  int64   `json:"contentLength"`
	InitStart      int64   `json:"initStart"`
	InitEnd        int64   `json:"initEnd"`
	IndexStart     int64   `json:"indexStart"`
	IndexEnd       int64   `json:"indexEnd"`
	DeliveryMethod string  `json:"deliveryMethod"`
	ManifestURL    *string `json:"manifestUrl"`
	SabrSessionURL *string `json:"sabrSessionUrl"`
}

type audioStream struct {
	URL            string  `json:"url"`
	MimeType       string  `json:"mimeType"`
	Format         string  `json:"format"`
	Bitrate        *int64  `json:"bitrate"`
	Codec          *string `json:"codec"`
	Quality        *string `json:"quality"`
	Itag           int     `json:"itag"`
	ContentLength  int64   `json:"contentLength"`
	InitStart      int64   `json:"initStart"`
	InitEnd        int64   `json:"initEnd"`
	IndexStart     int64   `json:"indexStart"`
	IndexEnd       int64   `json:"indexEnd"`
	AudioTrackID   *string `json:"audioTrackId"`
	AudioTrackName *string `json:"audioTrackName"`
	AudioLocale    *string `json:"audioLocale"`
	IsOriginal     bool    `json:"isOriginal"`
	DeliveryMethod string  `json:"deliveryMethod"`
	ManifestURL    *string `json:"manifestUrl"`
	SabrSessionURL *string `json:"sabrSessionUrl"`
}

type streamResponse struct {
	ID                           string              `json:"id"`
	Title                        string              `json:"title"`
	UploaderName                 string              `json:"uploaderName"`
	UploaderURL                  string              `json:"uploaderUrl"`
	UploaderAvatarURL            string              `json:"uploaderAvatarUrl"`
	ThumbnailURL                 string              `json:"thumbnailUrl"`
	Description                  string              `json:"description"`
	Duration                     int64               `json:"duration"`
	ViewCount                    int64               `json:"viewCount"`
	LikeCount                    int64               `json:"likeCount"`
	DislikeCount                 int64               `json:"dislikeCount"`
	UploadDate                   string              `json:"uploadDate"`
	Uploaded                     int64               `json:"uploaded"`
	UploaderSubscriberCount      int64               `json:"uploaderSubscriberCount"`
	UploaderVerified             bool                `json:"uploaderVerified"`
	Category                     string              `json:"category"`
	License                      string              `json:"license"`
	Visibility                   string              `json:"visibility"`
	Tags                         []string            `json:"tags"`
	StreamType                   string              `json:"streamType"`
	IsShortFormContent           bool                `json:"isShortFormContent"`
	RequiresMembership           bool                `json:"requiresMembership"`
	StartPosition                int64               `json:"startPosition"`
	StreamSegments               []youtube.Chapter   `json:"streamSegments"`
	HlsURL                       string              `json:"hlsUrl"`
	DashMpdURL                   string              `json:"dashMpdUrl"`
	VideoStreams                 []videoStream       `json:"videoStreams"`
	AudioStreams                 []audioStream       `json:"audioStreams"`
	OriginalAudioTrackID         *string             `json:"originalAudioTrackId"`
	PreferredDefaultAudioTrackID *string             `json:"preferredDefaultAudioTrackId"`
	VideoOnlyStreams             []videoStream       `json:"videoOnlyStreams"`
	Subtitles                    []youtube.Subtitle  `json:"subtitles"`
	PreviewFrames                []youtube.Frames    `json:"previewFrames"`
	SponsorBlockSegments         []sponsorSegment    `json:"sponsorBlockSegments"`
	SponsorBlockAvailable        bool                `json:"sponsorBlockAvailable"`
	RelatedStreams               []youtube.VideoItem `json:"relatedStreams"`
	PublishedAt                  *int64              `json:"publishedAt"`
	IsLive                       bool                `json:"isLive"`
	IsPostLive                   bool                `json:"isPostLive"`
	IsLiveContent                bool                `json:"isLiveContent"`
	HasLiveManifest              bool                `json:"hasLiveManifest"`
	Collections                  []any               `json:"collections"`
	Parts                        []any               `json:"parts"`
	EndScreenElements            []any               `json:"endScreenElements"`
}

var youtubeIDRe = regexp.MustCompile(`(?:[?&]v=|youtu\.be/|/shorts/|/embed/|/live/|/v/)([A-Za-z0-9_-]{11})`)

// YouTubeID reads the video id from any YouTube video URL.
func YouTubeID(raw string) string {
	if m := youtubeIDRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return ""
}

func startParam(raw string) int64 {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	t := u.Query().Get("t")
	if t == "" {
		t = u.Query().Get("start")
	}
	var total int64
	cur := int64(0)
	for _, c := range strings.TrimSuffix(t, "s") {
		switch {
		case c >= '0' && c <= '9':
			cur = cur*10 + int64(c-'0')
		case c == 'h':
			total, cur = total+cur*3600, 0
		case c == 'm':
			total, cur = total+cur*60, 0
		default:
			return 0
		}
	}
	return total + cur
}

func (s *Server) streamRoutes(rt *Router) {
	rt.Handle("GET", "/streams/youtube/sabr", func(w http.ResponseWriter, r *http.Request) { s.youtubeStream(w, r, false) })
	rt.Handle("GET", "/streams/youtube/live", func(w http.ResponseWriter, r *http.Request) { s.youtubeStream(w, r, true) })
	rt.Handle("GET", "/streams/youtube/sabr/bootstrap", s.youtubeBootstrap)
}

// youtubeBootstrap answers from the token session alone, like SabrBootstrapStreamService: what the player needs
// to start, without waiting for the related videos.
func (s *Server) youtubeBootstrap(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	id := YouTubeID(raw)
	if id == "" {
		httpx.Error(w, r, http.StatusBadRequest, "URL does not match stream endpoint", "provider_mismatch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sabrInfoTimeout)
	defer cancel()
	in, err := s.Sabr.Token().Info(ctx, id)
	if err != nil {
		s.Log.Warn("bootstrap", "videoId", id, "err", err)
		httpx.Error(w, r, http.StatusBadGateway, "SABR bootstrap metadata unavailable", "error")
		return
	}
	resp := buildStream(id, raw, nil, in)
	m := in.Meta
	resp.UploaderName, resp.UploaderAvatarURL, resp.Description = m.Author, m.ChannelAvatar, m.Description
	if m.ChannelID != "" {
		resp.UploaderURL = youtube.ChannelURL(m.ChannelID)
	}
	resp.ThumbnailURL, resp.ViewCount = m.ThumbnailURL, m.ViewCount
	if m.Tags != nil {
		resp.Tags = m.Tags
	}
	resp.IsLiveContent = m.IsLiveContent
	resp.SponsorBlockAvailable = false
	httpx.JSON(w, http.StatusOK, resp)
}

func codec(mime string) *string {
	_, after, ok := strings.Cut(mime, "codecs=")
	if !ok {
		return nil
	}
	c := strings.Trim(strings.TrimSpace(strings.SplitN(after, ";", 2)[0]), `"`)
	if c == "" {
		return nil
	}
	return &c
}

func formatName(container string) string {
	switch strings.ToLower(container) {
	case "video/webm":
		return "WEBM"
	case "video/mp4":
		return "MPEG_4"
	}
	_, sub, _ := strings.Cut(container, "/")
	return strings.ToUpper(sub)
}

func sabrStreams(id string, in *sabr.Info) ([]videoStream, []audioStream) {
	manifest := "/sabr/manifest/" + id
	vs, as := []videoStream{}, []audioStream{}
	for i := range in.Formats {
		f := &in.Formats[i]
		container := containerMime(f.MimeType)
		switch {
		case f.IsVideo() && in.SelectVideo(f.Itag) != nil:
			res := f.QualityLabel
			if res == "" && f.Height > 0 {
				res = strconv.Itoa(f.Height) + "p"
			}
			v := videoStream{MimeType: container, Format: formatName(container), Resolution: res, Codec: codec(f.MimeType),
				IsVideoOnly: true, Itag: f.Itag, Width: max(f.Width, 0), Height: max(f.Height, 0), DeliveryMethod: "sabr",
				ManifestURL: strp(manifest), SabrSessionURL: strp("/sabr/session/" + id + "?videoItag=" + strconv.Itoa(f.Itag))}
			if f.Bitrate > 0 {
				b := f.Bitrate
				v.Bitrate = &b
			}
			vs = append(vs, v)
		case f.IsAudio():
			track := ""
			if t := f.AudioTrackID(); t != nil {
				track = *t
			}
			if in.SelectAudio(f.Itag, track) == nil {
				continue
			}
			_, sub, _ := strings.Cut(container, "/")
			a := audioStream{MimeType: container, Format: strings.ToUpper(sub), Codec: codec(f.MimeType), Itag: f.Itag,
				AudioTrackID: f.AudioTrackID(), IsOriginal: f.IsOriginal(), DeliveryMethod: "sabr", ManifestURL: strp(manifest)}
			session := "/sabr/session/" + id + "?audioItag=" + strconv.Itoa(f.Itag)
			if track != "" {
				session += "&audioTrackId=" + url.QueryEscape(track)
				locale, _, _ := strings.Cut(track, ".")
				a.AudioLocale = &locale
			}
			if f.AudioTrack != nil && f.AudioTrack.DisplayName != "" {
				a.AudioTrackName = strp(f.AudioTrack.DisplayName)
			}
			a.SabrSessionURL = &session
			if f.Bitrate/1000 > 0 {
				b := f.Bitrate / 1000
				a.Bitrate = &b
			}
			if f.AudioQuality != "" {
				a.Quality = strp(f.AudioQuality)
			}
			as = append(as, a)
		}
	}
	return vs, as
}

func (s *Server) youtubeStream(w http.ResponseWriter, r *http.Request, live bool) {
	{
		raw := r.URL.Query().Get("url")
		if raw == "" {
			plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
			return
		}
		id := YouTubeID(raw)
		if id == "" {
			httpx.Error(w, r, http.StatusBadRequest, "URL does not match stream endpoint", "provider_mismatch")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		infoc := make(chan *sabr.Info, 1)
		go func() {
			in, err := s.Sabr.Token().Info(ctx, id)
			if err != nil {
				s.Log.Warn("stream sabr info", "videoId", id, "err", err)
			}
			infoc <- in
		}()
		sbc := make(chan sponsorResult, 1)
		go func() { sbc <- s.sponsor.segments(ctx, id) }()
		watch, err := s.YT.FetchWatch(ctx, id)
		in := <-infoc
		if err != nil && in == nil {
			s.Log.Warn("stream watch", "videoId", id, "err", err)
			httpx.Error(w, r, http.StatusBadGateway, "Could not extract this video", "extraction_failed")
			return
		}
		resp := buildStream(id, raw, watch, in)
		if watch != nil && watch.Related != nil {
			resp.RelatedStreams = watch.Related
		}
		sb := <-sbc
		resp.SponsorBlockSegments, resp.SponsorBlockAvailable = sb.segments, sb.available
		if live {
			if !resp.IsLive || resp.HlsURL == "" {
				httpx.Error(w, r, http.StatusBadGateway, "No active YouTube live stream is available", "live_stream_unavailable")
				return
			}
			resp.VideoStreams, resp.VideoOnlyStreams, resp.AudioStreams, resp.DashMpdURL = []videoStream{}, []videoStream{}, []audioStream{}, ""
		}
		if resp.IsLive && resp.HlsURL != "" {
			resp.HlsURL = s.hlsPath(resp.HlsURL)
		}
		httpx.JSON(w, http.StatusOK, resp)
	}
}

func buildStream(id, raw string, wt *youtube.Watch, in *sabr.Info) streamResponse {
	resp := streamResponse{ID: id, ViewCount: -1, LikeCount: -1, DislikeCount: -1, Uploaded: -1, UploaderSubscriberCount: -1,
		Visibility: "public", Tags: []string{}, StreamType: "video_stream", StreamSegments: []youtube.Chapter{},
		VideoStreams: []videoStream{}, AudioStreams: []audioStream{}, VideoOnlyStreams: []videoStream{}, Subtitles: []youtube.Subtitle{},
		PreviewFrames: []youtube.Frames{}, SponsorBlockSegments: []sponsorSegment{}, RelatedStreams: []youtube.VideoItem{},
		Collections: []any{}, Parts: []any{}, EndScreenElements: []any{}, StartPosition: startParam(raw)}
	if wt != nil {
		resp.Title, resp.Description, resp.UploaderName, resp.UploaderURL = wt.Title, wt.Description, wt.UploaderName, wt.UploaderURL
		resp.UploaderAvatarURL, resp.ThumbnailURL, resp.Duration = wt.UploaderAvatarURL, wt.ThumbnailURL, wt.Duration
		resp.ViewCount, resp.LikeCount, resp.UploadDate, resp.Uploaded = wt.ViewCount, wt.LikeCount, wt.UploadDate, wt.Uploaded
		resp.UploaderSubscriberCount, resp.UploaderVerified, resp.Category, resp.License = wt.Subscribers, wt.Verified, wt.Category, wt.License
		if wt.Unlisted {
			resp.Visibility = "unlisted"
		}
		if wt.Tags != nil {
			resp.Tags = wt.Tags
		}
		if wt.Chapters != nil {
			resp.StreamSegments = wt.Chapters
		}
		resp.Subtitles, resp.PreviewFrames = wt.Subtitles, wt.Frames
		if wt.Live {
			resp.StreamType, resp.IsLive, resp.IsLiveContent = "live_stream", true, true
		} else if wt.LiveContent {
			resp.IsLiveContent = true
		}
	}
	if in != nil {
		if resp.Title == "" {
			resp.Title = in.Title
		}
		if resp.Duration == 0 {
			resp.Duration = in.DurationMs / 1000
		}
		if !resp.IsLive {
			resp.VideoOnlyStreams, resp.AudioStreams = sabrStreams(id, in)
		} else if in.HlsURL != "" {
			resp.HlsURL, resp.HasLiveManifest = in.HlsURL, true
		}
	}
	if resp.Uploaded > 0 {
		v := resp.Uploaded
		resp.PublishedAt = &v
	}
	return resp
}
