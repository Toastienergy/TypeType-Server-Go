package api

import (
	"strings"
	"testing"

	"typetype.dev/server/internal/config"
)

func TestAudioToken(t *testing.T) {
	s := &Server{Cfg: &config.Config{JWTSecret: "secret"}}
	in := audioToken{UserID: "u1", VideoURL: "https://www.youtube.com/watch?v=M7lc1UVf-VE", Locale: "fr", TrackID: "fr.4", PreferOriginal: true, Itag: 140}
	raw := s.createAudioToken(in)
	got, msg := s.verifyAudioToken(raw)
	if msg != "" || got.VideoURL != in.VideoURL || got.Itag != 140 || got.TrackID != "fr.4" || !got.PreferOriginal || got.UserID != "u1" {
		t.Fatalf("round trip = %+v %q", got, msg)
	}
	if _, msg := s.verifyAudioToken(strings.Replace(raw, ".140.", ".251.", 1)); msg != "Invalid audio-only token" {
		t.Fatalf("tampered token accepted: %q", msg)
	}
	other := &Server{Cfg: &config.Config{JWTSecret: "other"}}
	if _, msg := other.verifyAudioToken(raw); msg == "" {
		t.Fatal("token from another secret accepted")
	}
}
