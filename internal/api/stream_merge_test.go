package api

import "testing"

func TestWithPublicMetadataFillsOnlyBlanks(t *testing.T) {
	signed := streamResponse{ID: "v", Title: "", Duration: 164, ViewCount: -1, LikeCount: 7, UploaderURL: "own",
		VideoOnlyStreams: []videoStream{{}}, SponsorBlockAvailable: true}
	public := &streamResponse{Title: "Public title", Duration: 999, ViewCount: 175000, LikeCount: 175, UploaderName: "Channel",
		UploaderURL: "public", Description: "d", SponsorBlockAvailable: false}
	got := withPublicMetadata(signed, public)
	if got.Title != "Public title" || got.UploaderName != "Channel" || got.Description != "d" || got.ViewCount != 175000 {
		t.Fatalf("blanks not filled: %+v", got)
	}
	if got.Duration != 164 || got.LikeCount != 7 || got.UploaderURL != "own" || len(got.VideoOnlyStreams) != 1 {
		t.Fatalf("signed-in values replaced: %+v", got)
	}
	if got.SponsorBlockAvailable {
		t.Fatal("SponsorBlock availability must need both answers, as in Kotlin")
	}
	if withPublicMetadata(signed, nil).Title != "" {
		t.Fatal("nil public answer changed the response")
	}
}
