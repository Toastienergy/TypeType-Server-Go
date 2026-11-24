package bilibili

import (
	"encoding/json"
	"testing"
)

func TestCollections(t *testing.T) {
	var watch map[string]any
	json.Unmarshal([]byte(`{"ugc_season":{"id":42,"title":"Course","sections":[{"id":7,"title":"Main","episodes":[
		{"bvid":"BV1xx411c7mD","title":"One","pic":"http://i0.hdslb.com/a.jpg"},
		{"aid":170001,"arc":{"title":"Two","pic":"http://i0.hdslb.com/b.jpg"}},
		{"bvid":"BV1yy411c7mE"}]},{"title":"no id"}]}}`), &watch)
	got, _ := json.Marshal(collections(watch))
	want := `[{"id":"42","sections":[{"episodes":[{"thumbnailUrl":"https://i0.hdslb.com/a.jpg","title":"One","url":"https://www.bilibili.com/video/BV1xx411c7mD","videoId":"BV1xx411c7mD"},` +
		`{"thumbnailUrl":"https://i0.hdslb.com/b.jpg","title":"Two","url":"https://www.bilibili.com/video/` + AV2BV(170001) + `","videoId":"` + AV2BV(170001) + `"}],"id":"7","title":"Main"}],"title":"Course"}]`
	if string(got) != want {
		t.Fatalf("%s", got)
	}
	if c := collections(map[string]any{}); len(c) != 0 {
		t.Fatal("no season")
	}
}
