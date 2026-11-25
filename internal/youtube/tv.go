package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	TVVersion = "7.20260901.15.00"
	TVUA      = "Mozilla/5.0 (SMART-TV; X11; Linux armv7l) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/90.0.4430.85 Safari/537.36"
	TVID      = 7
)

var tvPlayerURL = youtubeOrigin + "/youtubei/v1/player"

// TVPlayer is YoutubeTvOauthPlayerClient.player: the TV client /player of a device-code session, with the access
// token, the TV PO token and its living-room id, and an eight-digit signature timestamp.
func (c *Client) TVPlayer(ctx context.Context, accessToken, videoID, visitor, livingRoomID, poToken, cpn string, sts int64) (map[string]any, error) {
	client := map[string]any{"clientName": "TVHTML5", "clientVersion": TVVersion, "clientScreen": "WATCH", "userAgent": TVUA,
		"acceptLanguage": "en-US", "acceptRegion": "US", "utcOffsetMinutes": "0"}
	if visitor != "" {
		client["visitorData"] = visitor
	}
	if livingRoomID != "" {
		client["tvAppInfo"] = map[string]any{"livingRoomPoTokenId": livingRoomID}
	}
	body := map[string]any{
		"context": map[string]any{"client": client, "user": map[string]any{"enableSafetyMode": false, "lockedSafetyMode": false}},
		"playbackContext": map[string]any{
			"contentPlaybackContext": map[string]any{"html5Preference": "HTML5_PREF_WANTS", "lactMilliseconds": 60000,
				"isInlinePlaybackNoAd": true, "signatureTimestamp": sts},
			"devicePlaybackCapabilities": map[string]any{"supportsVp9Encoding": true, "supportXhr": false},
		},
		"videoId": videoID, "cpn": cpn, "racyCheckOk": true, "contentCheckOk": true,
	}
	if poToken != "" {
		body["serviceIntegrityDimensions"] = map[string]any{"poToken": poToken}
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", tvPlayerURL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	req.Header.Set("X-Youtube-Client-Name", "7")
	req.Header.Set("X-Youtube-Client-Version", TVVersion)
	req.Header.Set("Origin", youtubeOrigin)
	req.Header.Set("Referer", youtubeOrigin+"/tv")
	req.Header.Set("User-Agent", TVUA)
	if visitor != "" {
		req.Header.Set("X-Goog-Visitor-Id", visitor)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil, fmt.Errorf("%w %d on tv player", ErrStatus, res.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
