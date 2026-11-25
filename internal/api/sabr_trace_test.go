package api

import "testing"

func TestPrewarmMissNamesWhatDiffers(t *testing.T) {
	if got := prewarmMiss("u|v|137|140||0|false", nil); got != "none_prepared" {
		t.Fatal(got)
	}
	got := prewarmMiss("u|v|137|140||95000|false", []string{"u|v|136|140||0|false"})
	if got != "videoItag:136->137,startMs:0->95000" {
		t.Fatal(got)
	}
}
