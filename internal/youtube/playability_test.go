package youtube

import (
	"encoding/json"
	"testing"
)

func TestCheckPlayability(t *testing.T) {
	cases := map[string][2]string{
		`{"status":"OK"}`: {"", ""},
		`{"status":"LOGIN_REQUIRED","reason":"Sign in to confirm your age"}`:         {"age_restricted", "This age-restricted video cannot be watched anonymously"},
		`{"status":"LOGIN_REQUIRED","messages":["This is a private video."]}`:        {"private_content", "This video is private"},
		`{"status":"LOGIN_REQUIRED","reason":"Sign in to confirm you're not a bot"}`: {"provider_access_blocked", ""},
		`{"status":"ERROR","reason":"This video is unavailable"}`:                    {"content_unavailable", "This video is unavailable"},
		`{"status":"UNPLAYABLE","reason":"Video unavailable","errorScreen":{"playerErrorMessageRenderer":{"subreason":{"simpleText":"The uploader has not made this video available in your country"}}}}`: {"geographic_restriction", "This video is not available in client's country."},
		`{"status":"UNPLAYABLE","reason":"Join this channel to get access to members-only content like this video."}`:                                                                                     {"members_only", "This video is only available for members"},
		`{"status":"UNPLAYABLE","reason":"This video requires payment to watch."}`:                                                                                                                        {"paid_content", "This video is a paid video"},
		`{"status":"LIVE_STREAM_OFFLINE","reason":"This live event will begin in 3 hours."}`:                                                                                                              {"live_not_started", "This live event will begin in 3 hours."},
		`{"status":"LIVE_STREAM_OFFLINE","reason":"Premieres in 2 days"}`:                                                                                                                                 {"scheduled_premiere", "Premieres in 2 days"},
		`{"status":"ERROR","errorScreen":{"playerCaptchaViewModel":{}}}`:                                                                                                                                  {"provider_access_blocked", ""},
		`{"status":"WEIRD"}`: {"content_unavailable", `Got error: "null"`},
	}
	for in, want := range cases {
		var ps map[string]any
		json.Unmarshal([]byte(in), &ps)
		got := CheckPlayability(ps)
		if want[0] == "" {
			if got != nil {
				t.Errorf("%s: %+v", in, got)
			}
			continue
		}
		if got == nil || got.Code != want[0] || got.Message != want[1] {
			t.Errorf("%s: %+v", in, got)
		}
	}
}
