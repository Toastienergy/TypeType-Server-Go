package api

import (
	"slices"
	"testing"

	"typetype.dev/server/internal/youtube"
)

func TestCommentSearchQuery(t *testing.T) {
	got := parseCommentQuery(`  Great "Big  Buck" great İstanbul ＡＢＣ `)
	if !slices.Equal(got, []string{"great", "big buck", "istanbul", "abc"}) {
		t.Fatalf("%q", got)
	}
	if parseCommentQuery("   ") != nil || parseCommentQuery(string(make([]byte, 201))) != nil {
		t.Fatal("empty or long query accepted")
	}
	c := youtube.Comment{Author: "@Bunny", Text: "What a BIG\n\nbuck, great"}
	if !commentMatches(c, got[:2]) || commentMatches(c, []string{"bunny great", "x"}) {
		t.Fatal("match")
	}
}
