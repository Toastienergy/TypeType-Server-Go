package bilibili

import (
	"encoding/json"
	"testing"
)

func TestPickLiveFLVAndRoom(t *testing.T) {
	var f map[string]any
	json.Unmarshal([]byte(`{"codec":[{"codec_name":"hevc","base_url":"/h.flv","url_info":[{"host":"https://a","extra":"?x"}]},
		{"codec_name":"avc","base_url":"/a.flv","url_info":[{"host":"https://b","extra":"?y"}]}]}`), &f)
	if got := pickLiveFLV(f); got != "https://b/a.flv?y" {
		t.Fatalf("%s", got)
	}
	json.Unmarshal([]byte(`{"codec":[{"codec_name":"hevc","base_url":"/h.flv","url_info":[{"host":"https://a","extra":"?x"}]}]}`), &f)
	if got := pickLiveFLV(f); got != "https://a/h.flv?x" {
		t.Fatalf("%s", got)
	}
	for in, want := range map[string]string{"https://live.bilibili.com/22325806?spm=1": "22325806", "https://live.bilibili.com/blanc/1": "", "https://live.bilibili.com/": ""} {
		if got := liveRoomParam(in); got != want {
			t.Errorf("%s -> %q", in, got)
		}
	}
	if !IsLiveURL("https://live.bilibili.com/1") || IsLiveURL("https://www.bilibili.com/video/BV1") {
		t.Fatal("IsLiveURL")
	}
}
