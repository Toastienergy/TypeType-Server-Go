package api

import "testing"

func TestResumeFromMatchesTheWebPlayer(t *testing.T) {
	cases := []struct{ saved, duration, want int64 }{
		{0, 600_000, 0},
		{4_999, 600_000, 0},
		{95_000, 600_000, 95_000},
		{590_000, 600_000, 0},
		{3_420_000, 3_600_000, 0},
		{3_400_000, 3_600_000, 3_400_000},
		{95_000, 0, 95_000},
	}
	for _, c := range cases {
		if got := resumeFrom(c.saved, c.duration); got != c.want {
			t.Fatalf("resumeFrom(%d, %d) = %d, want %d", c.saved, c.duration, got, c.want)
		}
	}
}
