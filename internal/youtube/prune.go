package youtube

// channelSkip lists containers of a channel browse answer that no parser reads. A videos tab is about 480 KB, and
// these menus, palettes, hover overlays and logging blobs are about 60% of it; skipping them at the token level
// spares the maps and strings that made the subscription feed rebuild the biggest allocator of the server.
var channelSkip = map[string]bool{
	"menuButton": true, "themedPalette": true, "thumbnailHoverOverlayToggleActionsViewModel": true,
	"signalServiceEndpoint": true, "videoCommand": true, "clickTrackingParams": true, "trackingParams": true,
	"watchEndpointSupportedOnesieConfig": true, "loggingContext": true, "loggingDirectives": true, "topbar": true,
	"accessibilityContext": true, "frameworkUpdates": true, "animatedThumbnailOverlayViewModel": true, "lottieData": true,
	"animationActivationEntitySelectorType": true, "animationActivationEntityKey": true,
	"animationActivationTargetId": true, "webResponseContextExtensionData": true,
}

// nextSkip is channelSkip for a watch page's next answer (about 780 KB), plus its description panel copy, end
// screen overlays, merchandise shelf, consent interstitial and language list, which no parser reads either.
// frameworkUpdates stays: comments read their entities there.
// trackingSkip is what every answer carries for YouTube's own logging; nothing here reads it.
var trackingSkip = map[string]bool{
	"trackingParams": true, "clickTrackingParams": true, "loggingContext": true, "loggingDirectives": true,
}

// playerSkip leaves out the tracking, ads and player configuration of the WEB and ANDROID_VR player answers
// FetchWatch reads; the SABR session asks its own player for the configuration.
var playerSkip = map[string]bool{
	"playbackTracking": true, "playerConfig": true, "trackingParams": true, "clickTrackingParams": true,
	"adBreakHeartbeatParams": true, "paidContentOverlay": true, "attestation": true, "frameworkUpdates": true,
	"responseContext": true, "loggingContext": true,
}

var nextSkip = map[string]bool{
	"menuButton": true, "themedPalette": true, "thumbnailHoverOverlayToggleActionsViewModel": true,
	"signalServiceEndpoint": true, "videoCommand": true, "clickTrackingParams": true, "trackingParams": true,
	"watchEndpointSupportedOnesieConfig": true, "loggingContext": true, "loggingDirectives": true, "topbar": true,
	"accessibilityContext": true, "structuredDescriptionContentRenderer": true, "showSheetCommand": true,
	"playerOverlays": true, "merchandiseShelfRenderer": true, "interstitial": true, "languageList": true,
	"animatedThumbnailOverlayViewModel": true, "lottieData": true, "animationActivationEntitySelectorType": true,
	"animationActivationEntityKey": true, "animationActivationTargetId": true, "webResponseContextExtensionData": true,
	"styleRuns": true, "repeatButton": true, "shareButton": true, "dislikeButtonViewModel": true, "headerMapping": true,
	"macroMarkersListEntity": true, "modalEndpoint": true,
}

// decoders keep their read buffer between answers; duplicate member names are allowed, as encoding/json does
// (the last one wins), which also spares a name set per object.
