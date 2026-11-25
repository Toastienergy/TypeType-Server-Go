package api

import (
	"strings"
	"testing"
	"time"
)

func TestSubtitleMemoryStaysUnderItsByteBound(t *testing.T) {
	st := &subtitles{memory: map[string]subtitleEntry{}}
	big := strings.Repeat("x", 3<<20)
	for i := range 10 {
		st.remember(string(rune('a'+i)), subtitleEntry{Content: big, at: time.Now()})
	}
	st.remember("a", subtitleEntry{Content: "small", at: time.Now()})
	total := 0
	for _, e := range st.memory {
		total += len(e.Content)
	}
	if total != st.memBytes || total > subtitleMemoryBytes || st.memory["a"].Content != "small" {
		t.Fatalf("%d bytes kept, %d counted, %d entries", total, st.memBytes, len(st.memory))
	}
}
