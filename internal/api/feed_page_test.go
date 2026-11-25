package api

import (
	"slices"
	"strconv"
	"testing"

	"typetype.dev/server/internal/youtube"
)

// slowFeedPage is the former handler loop: filter the whole feed, reverse for oldest, slice the page.
func slowFeedPage(all []youtube.VideoItem, offset, limit int, oldest bool, keep func(*youtube.VideoItem) bool) ([]youtube.VideoItem, bool) {
	var picked []youtube.VideoItem
	for i := range all {
		if keep(&all[i]) {
			picked = append(picked, all[i])
		}
	}
	if oldest {
		slices.Reverse(picked)
	}
	end := min(offset+limit, len(picked))
	videos := []youtube.VideoItem{}
	if offset < len(picked) {
		videos = picked[offset:end]
	}
	return videos, end < len(picked)
}

func TestFeedPageMatchesTheFullFilter(t *testing.T) {
	var all []youtube.VideoItem
	for i := range 97 {
		all = append(all, youtube.VideoItem{ID: strconv.Itoa(i), IsLive: i%5 == 0, RequiresMembership: i%7 == 0})
	}
	filters := []func(*youtube.VideoItem) bool{
		func(*youtube.VideoItem) bool { return true },
		func(v *youtube.VideoItem) bool { return !v.IsLive },
		func(v *youtube.VideoItem) bool { return !v.IsLive && !v.RequiresMembership },
		func(*youtube.VideoItem) bool { return false },
	}
	for fi, keep := range filters {
		for _, oldest := range []bool{false, true} {
			for _, limit := range []int{1, 7, 30, 100} {
				for offset := 0; offset <= 100; offset += 3 {
					got, more := feedPage(all, offset, limit, oldest, keep)
					want, wantMore := slowFeedPage(all, offset, limit, oldest, keep)
					if more != wantMore || !slices.EqualFunc(got, want, func(a, b youtube.VideoItem) bool { return a.ID == b.ID }) {
						t.Fatalf("filter %d oldest %v limit %d offset %d: %d videos more %v, want %d more %v",
							fi, oldest, limit, offset, len(got), more, len(want), wantMore)
					}
				}
			}
		}
	}
}

func BenchmarkFeedPage(b *testing.B) {
	all := feedOf(1000).videos
	keep := func(v *youtube.VideoItem) bool { return !v.IsLive }
	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			slowFeedPage(all, 0, 30, false, keep)
		}
	})
	b.Run("page", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			feedPage(all, 0, 30, false, keep)
		}
	})
}
