package niconico

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Suggestions is NiconicoSuggestionExtractor: the candidates of the search box's expand endpoint.
func (c *Client) Suggestions(ctx context.Context, query string) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://sug.search.nicovideo.jp/suggestion/expand/"+url.QueryEscape(query), nil)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%w: suggestions HTTP %d", ErrUnavailable, res.StatusCode)
	}
	var body struct {
		Candidates []string `json:"candidates"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if body.Candidates == nil {
		body.Candidates = []string{}
	}
	return body.Candidates, nil
}
