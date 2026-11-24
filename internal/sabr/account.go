package sabr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// SessionTokens is the token service's answer to /potoken/session: the PO tokens bound to a signed-in visitor data.
type SessionTokens struct {
	VisitorData    string `json:"visitorData"`
	SessionPoToken string `json:"sessionBoundPoToken"`
	VisitorPoToken string `json:"visitorBoundPoToken"`
	VideoPoToken   string `json:"videoBoundPoToken"`
	StreamingPot   string `json:"streamingPot"`
}

func (t *Token) post(ctx context.Context, path string, body any, out any) error {
	if t.base == "" {
		return ErrNoToken
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", t.base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	res, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("token %s HTTP %d: %.160s", path, res.StatusCode, raw)
	}
	return json.Unmarshal(raw, out)
}

// SessionTokens is TypetypeTokenSabrTokenClient.fetchSession.
func (t *Token) SessionTokens(ctx context.Context, videoID, binding string) (*SessionTokens, error) {
	var out SessionTokens
	if err := t.post(ctx, "/potoken/session", map[string]any{"videoId": videoID, "sessionBinding": binding, "refreshVideo": false}, &out); err != nil {
		return nil, err
	}
	if out.SessionPoToken == "" && out.VisitorPoToken == "" {
		return nil, fmt.Errorf("token /potoken/session gave no session PO token")
	}
	return &out, nil
}

// Decoded is the token service's player decoder answer: the signature timestamp of the current player and the
// deciphered n parameters.
type Decoded struct {
	SignatureTimestamp int64             `json:"signatureTimestamp"`
	Throttling         map[string]string `json:"throttlingParameters"`
}

func (t *Token) Decode(ctx context.Context, clientName string, n []string) (*Decoded, error) {
	var out Decoded
	body := map[string]any{"throttlingParameters": n}
	if clientName != "" {
		body["clientName"] = clientName
	}
	if err := t.post(ctx, "/youtube/player/decoder", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InfoFromPlayer builds the SABR info of a web /player answer, the n parameter of its streaming URL already
// deciphered, with the video-bound PO token the media requests carry.
func InfoFromPlayer(videoID string, p map[string]any, baseURL, streamingPot, visitorData string, clientID int, clientVer, userAgent string) (*Info, error) {
	sd, _ := p["streamingData"].(map[string]any)
	if baseURL == "" || sd == nil {
		return nil, fmt.Errorf("player gave no SABR streaming URL")
	}
	var formats []Format
	if raw, err := json.Marshal(sd["adaptiveFormats"]); err == nil {
		json.Unmarshal(raw, &formats)
	}
	cfg, _ := dig(p, "playerConfig", "mediaCommonConfig", "mediaUstreamerRequestConfig")["videoPlaybackUstreamerConfig"].(string)
	vd := dig(p, "videoDetails")
	title, _ := vd["title"].(string)
	in := &Info{VideoID: videoID, BaseURL: baseURL, Ustreamer: b64any(cfg), PoToken: b64any(streamingPot), VisitorData: visitorData,
		ClientID: clientID, ClientVer: clientVer, UserAgent: userAgent, Formats: formats, Title: title}
	if live, _ := vd["isLive"].(bool); live {
		in.Live = true
	}
	if s, _ := vd["lengthSeconds"].(string); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			in.DurationMs = n * 1000
		}
	}
	if u, err := url.Parse(baseURL); err == nil {
		if e, err := strconv.ParseInt(u.Query().Get("expire"), 10, 64); err == nil {
			in.Expires = time.Unix(e, 0)
		}
	}
	for i := range in.Formats {
		in.Formats[i].lmt, _ = strconv.ParseUint(in.Formats[i].LastModified, 10, 64)
		if in.DurationMs == 0 {
			in.DurationMs = max(in.DurationMs, in.Formats[i].DurationMs.Int())
		}
	}
	return in, nil
}

func dig(m map[string]any, path ...string) map[string]any {
	for _, k := range path {
		next, _ := m[k].(map[string]any)
		if next == nil {
			return map[string]any{}
		}
		m = next
	}
	return m
}

// TVToken is the token service's TV PO token, as TypetypeTokenYoutubeSessionClient.fetchTvPoToken.
type TVToken struct {
	PoToken      string `json:"poToken"`
	VisitorData  string `json:"visitorData"`
	LivingRoomID string `json:"livingRoomPoTokenId"`
}

func (t *Token) TV(ctx context.Context) (*TVToken, error) {
	if t.base == "" {
		return nil, ErrNoToken
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", t.base+"/tv-token", nil)
	res, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out TVToken
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil ||
		out.PoToken == "" || out.VisitorData == "" || out.LivingRoomID == "" {
		return nil, fmt.Errorf("token /tv-token unavailable")
	}
	return &out, nil
}
