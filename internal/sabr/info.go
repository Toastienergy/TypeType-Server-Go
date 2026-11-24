package sabr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Format is one adaptive format of a SABR session, as the token service returns it.
type Format struct {
	Itag         int    `json:"itag"`
	LastModified string `json:"lastModified"`
	MimeType     string `json:"mimeType"`
	Xtags        string `json:"xtags"`
	IsDrc        bool   `json:"isDrc"`
	Bitrate      int64  `json:"bitrate"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	QualityLabel string `json:"qualityLabel"`
	AudioQuality string `json:"audioQuality"`
	DurationMs   flex   `json:"approxDurationMs"`
	AudioTrack   *struct {
		ID           string `json:"id"`
		DisplayName  string `json:"displayName"`
		AudioDefault bool   `json:"audioIsDefault"`
	} `json:"audioTrack"`
	lmt uint64
}

func (f *Format) IsAudio() bool { return strings.HasPrefix(f.MimeType, "audio/") }
func (f *Format) IsVideo() bool { return strings.HasPrefix(f.MimeType, "video/") }

func (f *Format) AudioTrackID() *string {
	if f.AudioTrack == nil || f.AudioTrack.ID == "" {
		return nil
	}
	id := f.AudioTrack.ID
	return &id
}

// IsOriginal follows YoutubeSabrFormat.isOriginalAudio: the localized display name says "original".
func (f *Format) IsOriginal() bool {
	if f.AudioTrack == nil {
		return false
	}
	n := f.AudioTrack.DisplayName
	return strings.Contains(n, "original") || strings.Contains(n, "yokuqala")
}

// Mime is the container and codecs for MSE, without the space the player JSON puts after the semicolon.
func (f *Format) Mime() string { return f.MimeType }

type flex string

func (v *flex) UnmarshalJSON(b []byte) error {
	*v = flex(strings.Trim(string(b), `"`))
	return nil
}

func (v flex) Int() int64 {
	n, _ := strconv.ParseInt(string(v), 10, 64)
	return n
}

// Info is everything a SABR pump needs to talk to googlevideo for one video.
type Info struct {
	VideoID     string
	BaseURL     string
	Ustreamer   []byte
	PoToken     []byte
	VisitorData string
	ClientID    int
	ClientVer   string
	UserAgent   string
	Formats     []Format
	DurationMs  int64
	Title       string
	Expires     time.Time
	Live        bool
}

const (
	mwebClientID = 2
	mwebUA       = "Mozilla/5.0 (iPad; CPU OS 16_7_10 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1,gzip(gfe)"
)

type tokenSession struct {
	Error         string   `json:"error"`
	URL           string   `json:"serverAbrStreamingUrl"`
	Ustreamer     string   `json:"videoPlaybackUstreamerConfig"`
	Formats       []Format `json:"adaptiveFormats"`
	Title         string   `json:"title"`
	StreamingPot  string   `json:"streamingPot"`
	VisitorData   string   `json:"visitorData"`
	DurationMs    int64    `json:"durationMs"`
	HlsManifest   *string  `json:"hlsManifestUrl"`
	TokenGenerate string   `json:"tokenGeneration"`
}

// b64any decodes standard or URL-safe base64, padded or not.
func b64any(s string) []byte {
	s = strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(s), "=")
	b, _ := base64.RawURLEncoding.DecodeString(s)
	return b
}

func parseSession(videoID string, raw []byte) (*Info, error) {
	var t tokenSession
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	if t.Error != "" || t.URL == "" {
		return nil, fmt.Errorf("token session: %s", t.Error)
	}
	in := &Info{VideoID: videoID, BaseURL: t.URL, Ustreamer: b64any(t.Ustreamer), PoToken: b64any(t.StreamingPot),
		VisitorData: t.VisitorData, ClientID: mwebClientID, ClientVer: "2.20250122.04.00", UserAgent: mwebUA,
		Formats: t.Formats, DurationMs: t.DurationMs, Title: t.Title, Live: t.HlsManifest != nil && *t.HlsManifest != ""}
	if u, err := url.Parse(t.URL); err == nil {
		if v := u.Query().Get("cver"); v != "" {
			in.ClientVer = v
		}
		if e, err := strconv.ParseInt(u.Query().Get("expire"), 10, 64); err == nil {
			in.Expires = time.Unix(e, 0)
		}
	}
	for i := range in.Formats {
		in.Formats[i].lmt, _ = strconv.ParseUint(in.Formats[i].LastModified, 10, 64)
	}
	if in.DurationMs == 0 {
		for _, f := range in.Formats {
			in.DurationMs = max(in.DurationMs, f.DurationMs.Int())
		}
	}
	return in, nil
}

// Token talks to TypeType-Token: it runs the MWEB player with a browser-minted PO token and answers ready
// sessions. Sessions are shared per video until ten minutes before their URL expires, and concurrent asks for
// the same video wait on one call.
type Token struct {
	base   string
	client *http.Client
	mu     sync.Mutex
	cache  map[string]*infoCall
}

type infoCall struct {
	done chan struct{}
	info *Info
	err  error
}

func NewToken(base string) *Token {
	return &Token{base: strings.TrimRight(base, "/"), cache: map[string]*infoCall{},
		client: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 16, IdleConnTimeout: 90 * time.Second}}}
}

var ErrNoToken = errors.New("token service not configured")

func (t *Token) Info(ctx context.Context, videoID string) (*Info, error) {
	if t.base == "" {
		return nil, ErrNoToken
	}
	t.mu.Lock()
	c := t.cache[videoID]
	if c != nil {
		select {
		case <-c.done:
			if c.err != nil || time.Until(c.info.Expires) < 10*time.Minute {
				c = nil
			}
		default:
		}
	}
	if c == nil {
		c = &infoCall{done: make(chan struct{})}
		t.cache[videoID] = c
		t.mu.Unlock()
		c.info, c.err = t.fetch(context.WithoutCancel(ctx), videoID, "")
		close(c.done)
		if c.err != nil {
			t.mu.Lock()
			if t.cache[videoID] == c {
				delete(t.cache, videoID)
			}
			t.mu.Unlock()
		}
	} else {
		t.mu.Unlock()
	}
	select {
	case <-c.done:
		return c.info, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Reload answers the playback params YouTube asked for in a reload part with a fresh session.
func (t *Token) Reload(ctx context.Context, videoID, params string) (*Info, error) {
	in, err := t.fetch(ctx, videoID, params)
	if err == nil {
		t.mu.Lock()
		c := &infoCall{done: make(chan struct{}), info: in}
		close(c.done)
		t.cache[videoID] = c
		t.mu.Unlock()
	}
	return in, err
}

func (t *Token) fetch(ctx context.Context, videoID, reload string) (*Info, error) {
	var req *http.Request
	if reload == "" {
		req, _ = http.NewRequestWithContext(ctx, "GET", t.base+"/youtube/sabr/session?client=MWEB&videoId="+url.QueryEscape(videoID), nil)
	} else {
		body, _ := json.Marshal(map[string]string{"videoId": videoID, "client": "MWEB", "reloadPlaybackParams": reload})
		req, _ = http.NewRequestWithContext(ctx, "POST", t.base+"/youtube/sabr/session/reload", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token session HTTP %d: %.200s", res.StatusCode, raw)
	}
	return parseSession(videoID, raw)
}
