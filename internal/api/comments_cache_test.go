package api

import "testing"

func TestCommentsTTLMatchesKotlin(t *testing.T) {
	cases := []struct {
		raw, page string
		want      int64
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "", 180},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "yt1.abc", 600},
		{"https://www.bilibili.com/video/BV1Vx421976K", "", 300},
		{"https://www.nicovideo.jp/watch/sm47084936", "", 300},
		{"https://www.bilibili.com/video/BV1Vx421976K", "2", 600},
	}
	for _, c := range cases {
		if got := commentsTTL(c.raw, c.page); got != c.want {
			t.Errorf("commentsTTL(%q, %q) = %d, want %d", c.raw, c.page, got, c.want)
		}
	}
}
