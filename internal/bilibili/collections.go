package bilibili

import (
	"strconv"
	"strings"
)

// collections is BilibiliCollectionParser.parse: the video's ugc_season as one collection of sections and
// episodes, mapped as StreamCollectionMappers maps it.
func collections(watch map[string]any) []any {
	season := obj(watch, "ugc_season")
	id, title := numericID(season, "id"), str(season, "title")
	if season == nil || id == "" || title == "" {
		return []any{}
	}
	sections := []any{}
	for _, x := range arr(season, "sections") {
		sec, _ := x.(map[string]any)
		sid, stitle := numericID(sec, "id"), sec["title"]
		st, ok := stitle.(string)
		if sec == nil || sid == "" || !ok {
			continue
		}
		sections = append(sections, map[string]any{"id": sid, "title": st, "episodes": episodes(arr(sec, "episodes"))})
	}
	return []any{map[string]any{"id": id, "title": title, "sections": sections}}
}

func episodes(list []any) []any {
	out := []any{}
	for _, x := range list {
		ep, _ := x.(map[string]any)
		if ep == nil {
			continue
		}
		arc := obj(ep, "arc")
		aid := num(ep["aid"])
		if _, ok := ep["aid"]; !ok {
			aid = -1
			if arc != nil {
				if _, ok := arc["aid"]; ok {
					aid = num(arc["aid"])
				}
			}
		}
		vid := str(ep, "bvid")
		if _, ok := ep["bvid"]; !ok && arc != nil {
			vid = str(arc, "bvid")
		}
		if vid == "" && aid > 0 {
			vid = AV2BV(aid)
		}
		title := str(ep, "title")
		if title == "" && arc != nil {
			title = str(arc, "title")
		}
		pic := str(ep, "pic")
		if pic == "" && arc != nil {
			pic = str(arc, "pic")
		}
		pic = strings.ReplaceAll(pic, "http:", "https:")
		if vid == "" || title == "" {
			continue
		}
		out = append(out, map[string]any{"videoId": vid, "title": title, "url": "https://www.bilibili.com/video/" + vid, "thumbnailUrl": pic})
	}
	return out
}

func numericID(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	n := num(v)
	if n < 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}
