package api

import (
	"fmt"
	"reflect"
	"testing"

	"typetype.dev/server/internal/youtube"
)

func feedOf(videos int) *feedSnapshot {
	f := &feedSnapshot{sources: map[string][]string{}}
	for i := range videos {
		published := int64(1_790_000_000 + i*600)
		ch := fmt.Sprintf("https://www.youtube.com/channel/UC%022d", i%17)
		f.videos = append(f.videos, youtube.VideoItem{URL: fmt.Sprintf("https://www.youtube.com/watch?v=v%010d", i),
			Title: "A video", UploaderURL: ch, UploaderName: fmt.Sprintf("Channel %d", i%17), PublishedAt: &published})
	}
	return f
}

func enabledChannels(n int) map[string]bool {
	m := map[string]bool{}
	for i := range n {
		m[fmt.Sprintf("https://www.youtube.com/channel/UC%022d", i)] = true
	}
	return m
}

func TestNotificationItemsAreKeptPerSnapshotAndChannels(t *testing.T) {
	f := feedOf(200)
	first := f.notificationItems(enabledChannels(5))
	if len(first) == 0 || !reflect.DeepEqual(first, f.buildNotificationItems(enabledChannels(5))) {
		t.Fatalf("%d items, differs from a fresh build", len(first))
	}
	if again := f.notificationItems(enabledChannels(5)); &again[0] != &first[0] {
		t.Fatal("same channels rebuilt the list")
	}
	more := f.notificationItems(enabledChannels(9))
	if len(more) <= len(first) {
		t.Fatalf("more channels gave %d items, fewer channels %d", len(more), len(first))
	}
	for i := 1; i < len(more); i++ {
		if more[i].CreatedAt > more[i-1].CreatedAt {
			t.Fatal("items not newest first")
		}
	}
}

func BenchmarkNotificationItems(b *testing.B) {
	f, enabled := feedOf(1000), enabledChannels(17)
	b.Run("build", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			f.buildNotificationItems(enabled)
		}
	})
	b.Run("kept", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			f.notificationItems(enabled)
		}
	})
}
