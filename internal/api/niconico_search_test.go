package api

import "testing"

func TestNiconicoLiveItem(t *testing.T) {
	it := niconicoLiveItem(map[string]any{"title": "Live", "watchPageUrl": "https://live.nicovideo.jp/watch/lv1", "listingThumbnail": "https://t",
		"beginTime": float64(1795618055), "statistics": map[string]any{"watchCount": float64(7)}, "description": "d",
		"supplier": map[string]any{"name": "Me", "programProviderId": "12", "icons": map[string]any{"uri150x150": "https://a"}}})
	if it.ID != it.URL || it.UploaderURL != "https://www.nicovideo.jp/user/12" || it.UploaderAvatarURL != "https://a" || it.ViewCount != 7 ||
		!it.IsLive || it.Duration != -1 || it.UploadDate != "2026-11-25T23:47:35+09:00" || it.Uploaded != 1795618055000 {
		t.Fatalf("%+v", it)
	}
	group := niconicoLiveItem(map[string]any{"socialGroup": map[string]any{"id": "ch9", "name": "Chan", "thumbnailUrl": "https://g"},
		"beginTime": float64(1795617600)})
	if group.UploaderURL != "https://ch.nicovideo.jp/ch9" || group.UploaderName != "Chan" || group.UploadDate != "2026-11-25T23:40+09:00" {
		t.Fatalf("%+v", group)
	}
}

func TestNiconicoListItem(t *testing.T) {
	p := niconicoListItem(map[string]any{"id": float64(61271810), "title": "List", "thumbnailUrl": "https://t", "videoCount": float64(173),
		"owner": map[string]any{"id": "21514289", "name": "Owner"}})
	if p.URL != "https://www.nicovideo.jp/user/21514289/mylist/61271810" || p.ID != p.URL || p.Title != "List" || p.StreamCount != 173 ||
		p.PlaylistType != "normal" {
		t.Fatalf("%+v", p)
	}
}
