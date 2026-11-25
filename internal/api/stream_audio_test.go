package api

import "testing"

func audioTrack(id, name, locale string) audioStream {
	a := audioStream{}
	if id != "" {
		a.AudioTrackID = &id
	}
	if name != "" {
		a.AudioTrackName = &name
	}
	if locale != "" {
		a.AudioLocale = &locale
	}
	return a
}

func TestAudioContractLikeKotlin(t *testing.T) {
	r := streamResponse{AudioStreams: []audioStream{audioTrack("fr.0", "fr", "fr"), audioTrack("en-US.4", "en-US (original)", "en")}}
	resolveAudioContract(&r, "es")
	if deref(r.OriginalAudioTrackID) != "en-US.4" || deref(r.PreferredDefaultAudioTrackID) != "en-US.4" || r.AudioStreams[0].IsOriginal || !r.AudioStreams[1].IsOriginal {
		t.Fatalf("original: %+v", r)
	}
	r = streamResponse{AudioStreams: []audioStream{audioTrack("fr.0", "fr", "fr"), audioTrack("en.0", "en", "en")}}
	resolveAudioContract(&r, "en")
	if r.OriginalAudioTrackID != nil || deref(r.PreferredDefaultAudioTrackID) != "en.0" {
		t.Fatalf("fallback: %+v", r)
	}
	r = streamResponse{AudioStreams: []audioStream{audioTrack("", "und", ""), audioTrack("de.0", "de", "de")}}
	resolveAudioContract(&r, "en")
	if r.OriginalAudioTrackID != nil || deref(r.PreferredDefaultAudioTrackID) != "de.0" {
		t.Fatalf("first: %+v", r)
	}
}
