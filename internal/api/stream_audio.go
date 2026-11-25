package api

import (
	"os"
	"strings"
)

// applyAudioContract is Kotlin's StreamAudioContractResolver: a track whose display name says it is the original
// becomes the original and the preferred default; without one, the fallback language (DEFAULT_AUDIO_FALLBACK_LANGUAGE,
// English by default), then the first track with an id. Clients start on the preferred default, so without it a video
// dubbed in thirty languages started on the first one of the list.
func applyAudioContract(r *streamResponse) {
	fallback := strings.TrimSpace(os.Getenv("DEFAULT_AUDIO_FALLBACK_LANGUAGE"))
	if fallback == "" {
		fallback = "en"
	}
	resolveAudioContract(r, fallback)
}

func resolveAudioContract(r *streamResponse, fallback string) {
	var original, preferred *string
	for i := range r.AudioStreams {
		a := &r.AudioStreams[i]
		a.IsOriginal = isOriginalTrack(a.AudioTrackName)
		if original == nil && a.IsOriginal && a.AudioTrackID != nil {
			original = a.AudioTrackID
		}
	}
	preferred = original
	if preferred == nil {
		wanted := audioLanguage(fallback)
		for _, a := range r.AudioStreams {
			if a.AudioTrackID == nil {
				continue
			}
			if a.AudioLocale != nil && audioLanguage(*a.AudioLocale) == wanted ||
				audioLanguage(strings.SplitN(*a.AudioTrackID, ".", 2)[0]) == wanted {
				preferred = a.AudioTrackID
				break
			}
		}
	}
	if preferred == nil {
		for _, a := range r.AudioStreams {
			if a.AudioTrackID != nil {
				preferred = a.AudioTrackID
				break
			}
		}
	}
	r.OriginalAudioTrackID, r.PreferredDefaultAudioTrackID = original, preferred
}

func isOriginalTrack(name *string) bool {
	if name == nil {
		return false
	}
	n := strings.ToLower(*name)
	return strings.Contains(n, "original") || strings.Contains(n, "default") || strings.Contains(n, "yokuqala")
}

func audioLanguage(tag string) string {
	return strings.SplitN(strings.ToLower(tag), "-", 2)[0]
}
