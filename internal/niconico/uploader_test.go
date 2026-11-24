package niconico

import (
	"encoding/json"
	"testing"
)

func TestUploader(t *testing.T) {
	for raw, want := range map[string][3]string{
		`{"metadata":{"jsonLd":{"owner":{"id":"4","type":"user","name":"n","iconUrl":"i"}}}}`:           {"n", "https://www.nicovideo.jp/user/4", "i"},
		`{"metadata":{"jsonLd":{"owner":{"id":"ch2632720","type":"channel","name":"c"}}},"channel":{}}`: {"c", "https://ch.nicovideo.jp/ch2632720", ""},
		`{"owner":{"id":9,"nickname":"old","iconUrl":"oi"}}`:                                            {"old", "https://www.nicovideo.jp/user/9", "oi"},
		`{"owner":null,"channel":{"id":"ch1","name":"oc","thumbnail":{"url":"ct"}}}`:                    {"oc", "https://ch.nicovideo.jp/ch1", "ct"},
	} {
		var w map[string]any
		json.Unmarshal([]byte(raw), &w)
		name, u, icon := uploader(w)
		if [3]string{name, u, icon} != want {
			t.Errorf("%s -> %q %q %q", raw, name, u, icon)
		}
	}
}
