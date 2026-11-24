package youtube

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRunsHTML(t *testing.T) {
	var obj map[string]any
	json.Unmarshal([]byte(`{"runs":[{"text":"Tom's <b>  x\n"},{"text":"site","bold":true,"navigationEndpoint":{"urlEndpoint":{"url":"https://www.youtube.com/redirect?event=x&q=https%3A%2F%2Fnasa.gov%2Fa%3Fb%3D1"}}},
		{"emoji":{"shortcuts":[":rocket:"],"emojiId":"x"}},{"text":"@NASA","navigationEndpoint":{"browseEndpoint":{"browseId":"UCLA","canonicalBaseUrl":"/@NASA"}}},
		{"text":"h","navigationEndpoint":{"urlEndpoint":{"url":"https://www.youtube.com/@handle"}}},
		{"text":"w","navigationEndpoint":{"urlEndpoint":{"url":"/nothing"},"watchEndpoint":{"videoId":"abc","startTimeSeconds":5}}}]}`), &obj)
	want := `Tom&apos;s &lt;b&gt; &nbsp;x<br><b><a href="https://nasa.gov/a?b=1">site</a></b>:rocket:<a href="https://www.youtube.com/channel/UCLA">@NASA</a>` +
		`<a href="https://www.youtube.com/@handle">h</a><a href="https://www.youtube.com/watch?v=abc&t=5">w</a>`
	if got := runsHTML(obj); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := runsHTML(map[string]any{"simpleText": "a  b\n "}); got != "a  b<br>&nbsp;" {
		t.Fatalf("simple %q", got)
	}
	if jsoupEscape("\x01\t\"") != "&#x1;\t&quot;" {
		t.Fatal("escape")
	}
}

func TestParsePost(t *testing.T) {
	var p map[string]any
	json.Unmarshal([]byte(`{"postId":"Ug1","authorText":{"runs":[{"text":"NASA"}]},"authorEndpoint":{"browseEndpoint":{"browseId":"UCLA"}},
		"publishedTimeText":{"runs":[{"text":"2 days ago (edited)"}]},"voteCount":{"simpleText":"1.2K"},
		"backstageAttachment":{"pollRenderer":{"type":"POLL_TYPE_QUIZ","totalVotes":{"simpleText":"3 votes"},"choices":[{"text":{"runs":[{"text":"Yes"}]}}]}}}`), &p)
	post, ok := parsePost(p, time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC))
	if !ok || post.Type != "poll" || post.AuthorURL != "https://www.youtube.com/channel/UCLA" || *post.LikeCount != 1200 || post.CommentCount != nil ||
		post.Poll.Type != "quiz" || *post.Poll.VoteCount != 3 || post.Poll.Choices[0].ImageURL != nil || post.PublishedAt == nil {
		t.Fatalf("%+v %+v", post, post.Poll)
	}
}
