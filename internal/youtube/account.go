package youtube

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Account is a signed-in YouTube session: the cookie header of youtube.com and the account index (X-Goog-AuthUser).
type Account struct {
	Cookies  string
	AuthUser int
}

func (a Account) cookie(name string) string {
	for _, part := range strings.Split(a.Cookies, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k == name {
			return v
		}
	}
	return ""
}

const youtubeOrigin = "https://www.youtube.com"

// authorization is the SAPISIDHASH header a signed-in web page sends: for each SAPISID cookie, the time and the SHA-1
// of "time SAPISID origin".
func (a Account) authorization(now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	var out []string
	for _, p := range [][2]string{{"SAPISIDHASH", "SAPISID"}, {"SAPISID1PHASH", "__Secure-1PAPISID"}, {"SAPISID3PHASH", "__Secure-3PAPISID"}} {
		if v := a.cookie(p[1]); v != "" {
			sum := sha1.Sum([]byte(ts + " " + v + " " + youtubeOrigin))
			out = append(out, p[0]+" "+ts+"_"+hex.EncodeToString(sum[:]))
		}
	}
	return strings.Join(out, " ")
}

// accountCall is Call as the signed-in web client: the account's cookies and SAPISIDHASH, and its visitor data.
func (c *Client) accountCall(ctx context.Context, a Account, endpoint, visitor string, body map[string]any) (map[string]any, error) {
	client := map[string]any{"clientName": Web.Name, "clientVersion": Web.Version, "hl": "en", "gl": "US"}
	if visitor != "" {
		client["visitorData"] = visitor
	}
	body["context"] = map[string]any{"client": client}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", youtubeOrigin+"/youtubei/v1/"+endpoint+"?prettyPrint=false", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", Web.UA)
	req.Header.Set("X-YouTube-Client-Name", strconv.Itoa(Web.ID))
	req.Header.Set("X-YouTube-Client-Version", Web.Version)
	req.Header.Set("Cookie", a.Cookies)
	req.Header.Set("Authorization", a.authorization(time.Now()))
	req.Header.Set("X-Goog-AuthUser", strconv.Itoa(a.AuthUser))
	req.Header.Set("X-Origin", youtubeOrigin)
	req.Header.Set("Origin", youtubeOrigin)
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
		return nil, fmt.Errorf("%w %d on %s", ErrStatus, res.StatusCode, endpoint)
	}
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// AccountVisitorData is AuthenticatedYoutubeVisitorData: the visitor data YouTube gives the signed-in web client,
// which binds the session PO token.
func (c *Client) AccountVisitorData(ctx context.Context, a Account) (string, error) {
	out, err := c.accountCall(ctx, a, "visitor_id", "", map[string]any{})
	if err != nil {
		return "", err
	}
	if v := Str(out, "responseContext", "visitorData"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("no visitor data for the account")
}

// AccountPlayer is the signed-in web /player with the session PO token and the player's signature timestamp, as
// PipePipe's fetchSabrInfo with YoutubeSessionTokenScope.
func (c *Client) AccountPlayer(ctx context.Context, a Account, videoID, visitor, poToken string, sts int64) (map[string]any, error) {
	return c.accountCall(ctx, a, "player", visitor, map[string]any{
		"videoId": videoID, "contentCheckOk": true, "racyCheckOk": true,
		"playbackContext":            map[string]any{"contentPlaybackContext": map[string]any{"signatureTimestamp": sts, "html5Preference": "HTML5_PREF_WANTS"}},
		"serviceIntegrityDimensions": map[string]any{"poToken": poToken},
	})
}
