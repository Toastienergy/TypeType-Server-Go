package api

import (
	"context"
	"testing"
)

func TestBilibiliLiveCommentsAreDisabled(t *testing.T) {
	page, cerr := (&Server{}).bilibiliComments(context.Background(), "https://live.bilibili.com/21452505", "")
	if cerr != nil || !page.CommentsDisabled || len(page.Comments) != 0 {
		t.Fatalf("live comments: %+v %+v", page, cerr)
	}
}
