package youtube

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func playerFixtures(tb testing.TB) (web, vr []byte) {
	prefix := os.Getenv("TEST_PLAYER_FIXTURE")
	if prefix == "" {
		tb.Skip("TEST_PLAYER_FIXTURE not set")
	}
	web, err := os.ReadFile(prefix + "-web.json")
	if err != nil {
		tb.Fatal(err)
	}
	vr, err = os.ReadFile(prefix + "-vr.json")
	if err != nil {
		tb.Fatal(err)
	}
	return web, vr
}

func watchFromPlayers(web, vr map[string]any) *Watch {
	w := parsePlayer("eMlx5fFNoYc", web)
	w.Frames, w.Subtitles = parseFrames(vr), parseCaptions(vr)
	w.Title = Str(vr, "videoDetails", "title")
	return w
}

func TestPrunedPlayerMatchesFull(t *testing.T) {
	webRaw, vrRaw := playerFixtures(t)
	var web, vr map[string]any
	if json.Unmarshal(webRaw, &web) != nil || json.Unmarshal(vrRaw, &vr) != nil {
		t.Fatal("fixtures do not decode")
	}
	pweb, err := decodePruned(bytes.NewReader(webRaw), playerSkip)
	if err != nil {
		t.Fatal(err)
	}
	pvr, err := decodePruned(bytes.NewReader(vrRaw), playerSkip)
	if err != nil {
		t.Fatal(err)
	}
	full, pruned := watchFromPlayers(web, vr), watchFromPlayers(pweb, pvr)
	if full.Title == "" || len(full.Subtitles) == 0 || len(full.Frames) == 0 || !reflect.DeepEqual(full, pruned) {
		t.Fatalf("pruned player differs: %+v", pruned)
	}
}

func BenchmarkPlayer(b *testing.B) {
	_, vr := playerFixtures(b)
	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var v map[string]any
			json.Unmarshal(vr, &v)
		}
	})
	b.Run("pruned", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			decodePruned(bytes.NewReader(vr), playerSkip)
		}
	})
}
