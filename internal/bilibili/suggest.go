package bilibili

import (
	"context"
	"net/url"
)

// Suggestions is BilibiliSuggestionExtractor: the "value" of each tag the search box suggests; an answer without tags
// is an empty list.
func (c *Client) Suggestions(ctx context.Context, query string) ([]string, error) {
	res, err := c.Get(ctx, "https://s.search.bilibili.com/main/suggest?term="+url.QueryEscape(query), wwwReferer)
	if err != nil {
		return nil, err
	}
	out := []string{}
	result, _ := res["result"].(map[string]any)
	tags, _ := result["tag"].([]any)
	for _, t := range tags {
		if o, ok := t.(map[string]any); ok {
			if v, ok := o["value"].(string); ok {
				out = append(out, v)
			}
		}
	}
	return out, nil
}
