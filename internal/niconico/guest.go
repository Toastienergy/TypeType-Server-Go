package niconico

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"time"
)

// guestWatch reads the watch data from the guest API the NicoNico web player calls: the same data as the watch page,
// a quarter of its size and nothing to unescape. The page stays the fallback, which also tells why a video is closed.
func (c *Client) guestWatch(ctx context.Context, id string) (map[string]any, error) {
	url := "https://www.nicovideo.jp/api/watch/v3_guest/" + id + "?_frontendId=6&_frontendVersion=0&actionTrackId=" + actionTrackID()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", firefoxUA)
	req.Header.Set("Accept-Language", "ja-JP")
	req.Header.Set("X-Frontend-Id", "6")
	req.Header.Set("X-Frontend-Version", "0")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("niconico guest watch: HTTP %d", res.StatusCode)
	}
	var root struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&root); err != nil {
		return nil, err
	}
	if root.Data == nil || root.Data["media"] == nil {
		return nil, fmt.Errorf("niconico guest watch: no media")
	}
	return root.Data, nil
}

func actionTrackID() string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 10)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		b[i] = letters[n.Int64()]
	}
	return string(b) + "_" + strconv.FormatInt(time.Now().UnixMilli(), 10)
}
