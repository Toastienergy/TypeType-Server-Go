package api

import (
	"testing"

	"typetype.dev/server/internal/sabr"
	"typetype.dev/server/internal/youtube"
)

func TestTVSignatureAndInfo(t *testing.T) {
	if tvSignature(20779) != 20779001 || tvSignature(20779001) != 20779001 {
		t.Fatal("signature timestamp")
	}
	player := map[string]any{"streamingData": map[string]any{"adaptiveFormats": []any{map[string]any{"itag": 137, "mimeType": "video/mp4; codecs=\"avc1\"", "lastModified": "12"}}},
		"videoDetails": map[string]any{"lengthSeconds": "165", "title": "t"},
		"playerConfig": map[string]any{"mediaCommonConfig": map[string]any{"mediaUstreamerRequestConfig": map[string]any{"videoPlaybackUstreamerConfig": "AQID"}}}}
	in, err := sabr.InfoFromPlayer("vid", player, "https://rr1.googlevideo.com/videoplayback?expire=1900000000", "cG90", "vis", youtube.TVID, youtube.TVVersion, youtube.TVUA)
	if err != nil || in.ClientID != 7 || in.DurationMs != 165000 || len(in.Formats) != 1 || len(in.Ustreamer) != 3 || string(in.PoToken) != "pot" {
		t.Fatalf("%+v %v", in, err)
	}
}
