package api

import "strings"

// withPublicMetadata is Kotlin's withPublicStreamMetadata: a signed-in answer keeps its own streams and fills what
// its player left blank from the public page. The TVHTML5 player of a device-code session has no title, author,
// description or counts at all.
func withPublicMetadata(p streamResponse, m *streamResponse) streamResponse {
	if m == nil {
		return p
	}
	text := func(a, b string) string {
		if strings.TrimSpace(a) == "" {
			return b
		}
		return a
	}
	count := func(a, b int64) int64 {
		if a < 0 {
			return b
		}
		return a
	}
	p.Title = text(p.Title, m.Title)
	p.ThumbnailURL = text(p.ThumbnailURL, m.ThumbnailURL)
	p.Description = text(p.Description, m.Description)
	if p.Duration <= 0 {
		p.Duration = m.Duration
	}
	p.ViewCount, p.LikeCount, p.DislikeCount = count(p.ViewCount, m.ViewCount), count(p.LikeCount, m.LikeCount), count(p.DislikeCount, m.DislikeCount)
	p.UploadDate, p.Uploaded = text(p.UploadDate, m.UploadDate), count(p.Uploaded, m.Uploaded)
	p.UploaderName, p.UploaderURL = text(p.UploaderName, m.UploaderName), text(p.UploaderURL, m.UploaderURL)
	p.UploaderAvatarURL = text(p.UploaderAvatarURL, m.UploaderAvatarURL)
	p.UploaderSubscriberCount = count(p.UploaderSubscriberCount, m.UploaderSubscriberCount)
	p.UploaderVerified = p.UploaderVerified || m.UploaderVerified
	p.Category, p.License, p.Visibility = text(p.Category, m.Category), text(p.License, m.License), text(p.Visibility, m.Visibility)
	p.StreamType = text(p.StreamType, m.StreamType)
	p.IsPostLive, p.IsLiveContent = p.IsPostLive || m.IsPostLive, p.IsLiveContent || m.IsLiveContent
	p.IsShortFormContent, p.RequiresMembership = p.IsShortFormContent || m.IsShortFormContent, p.RequiresMembership || m.RequiresMembership
	if len(p.Tags) == 0 {
		p.Tags = m.Tags
	}
	if len(p.StreamSegments) == 0 {
		p.StreamSegments = m.StreamSegments
	}
	if p.OriginalAudioTrackID == nil {
		p.OriginalAudioTrackID = m.OriginalAudioTrackID
	}
	if p.PreferredDefaultAudioTrackID == nil {
		p.PreferredDefaultAudioTrackID = m.PreferredDefaultAudioTrackID
	}
	if len(p.Subtitles) == 0 {
		p.Subtitles = m.Subtitles
	}
	if len(p.PreviewFrames) == 0 {
		p.PreviewFrames = m.PreviewFrames
	}
	if len(p.SponsorBlockSegments) == 0 {
		p.SponsorBlockSegments = m.SponsorBlockSegments
	}
	p.SponsorBlockAvailable = p.SponsorBlockAvailable && m.SponsorBlockAvailable
	if len(p.RelatedStreams) == 0 {
		p.RelatedStreams = m.RelatedStreams
	}
	if p.PublishedAt == nil {
		p.PublishedAt = m.PublishedAt
	}
	if len(p.Collections) == 0 {
		p.Collections = m.Collections
	}
	if len(p.Parts) == 0 {
		p.Parts = m.Parts
	}
	if len(p.EndScreenElements) == 0 {
		p.EndScreenElements = m.EndScreenElements
	}
	return p
}
