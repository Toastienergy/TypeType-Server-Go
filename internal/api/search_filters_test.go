package api

import "testing"

func TestYoutubeSP(t *testing.T) {
	tree := youtubeFilters
	pick := func(content string, sorts ...string) string {
		c, msg := tree.resolveContent(content, content != "")
		if msg != "" {
			t.Fatal(msg)
		}
		s, msg := resolveSort(tree.sortFor(c), sorts)
		if msg != "" {
			t.Fatal(msg)
		}
		return youtubeSP(c[0], s)
	}
	cases := []struct{ got, want string }{
		{pick(""), "CAASAA%3D%3D"},
		{pick("|1|videos"), "CAASAhAB"},
		{pick("|2|channels"), "CAASAhAC"},
		{pick("|1|videos", "sortby|13|sort_view", "upload_date|17|past_week"), "CAMSBAgDEAE%3D"},
		{pick("|1|videos", "features|23|HD", "features|31|Location"), "EgcQASABuAEB"},
		{pick("|0|all", "upload_date|14|all"), "8AEB"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: sp = %s, want %s", i, c.got, c.want)
		}
	}
}

func TestFilterValues(t *testing.T) {
	r := youtubeFilters.response(nil)
	content := r["contentFilters"].([]filterOption)
	if content[0] != (filterOption{"|0|all", "all", true}) || content[5] != (filterOption{"YouTube Music|6|music_videos", "YouTube Music: music_videos", false}) {
		t.Fatalf("content = %+v", content)
	}
	if _, msg := resolveSort(youtubeFilters.all, []string{"sortby|11|sort_relevance", "sortby|12|sort_rating"}); msg != "Only one 'sortby' filter can be selected" {
		t.Fatalf("conflict = %q", msg)
	}
	if _, msg := bilibiliFilters.resolveContent("|4|movies_and_tv", true); msg != "" {
		t.Fatal(msg)
	}
	if _, msg := niconicoFilters.resolveContent("", true); msg != "Unknown content filter" {
		t.Fatalf("blank filter = %q", msg)
	}
	groups := niconicoFilters.sortFor([]*filterItem{niconicoFilters.content[0].items[2]})
	if len(groups) != 1 || groups[0].items[0].query != "sortKey=_hotTotalScore" {
		t.Fatalf("playlist variant = %+v", groups)
	}
}
