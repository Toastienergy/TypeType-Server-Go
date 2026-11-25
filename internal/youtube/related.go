package youtube

import (
	"context"
	"time"
)

// Related reads only the watch page's next response, which is all the recommendation pool needs from a seed video.
func (c *Client) Related(ctx context.Context, id string) ([]VideoItem, error) {
	n, err := c.call(ctx, Web, "next", map[string]any{"videoId": id, "contentCheckOk": true, "racyCheckOk": true}, nextSkip)
	if err != nil {
		return nil, err
	}
	w := &Watch{}
	parseNext(w, n, time.Now())
	return w.Related, nil
}
