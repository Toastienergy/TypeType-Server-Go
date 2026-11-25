package recommender

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Result is RecommenderCallResult: a JSON object, a 409 the service sends when its copy of the history expired, or
// a failure.
type Result struct {
	Body    map[string]any
	Expired bool
	Err     string
}

func (r Result) OK() bool { return r.Body != nil }

// Client is RecommenderClient.
type Client struct {
	cfg  Config
	http *http.Client
}

func NewClient(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{cfg: cfg, http: hc}
}

const maxLocaleChars = 12

func (c *Client) Health(ctx context.Context) bool {
	return c.get(ctx, "/health", c.cfg.RequestTimeout).OK()
}

func (c *Client) Version(ctx context.Context) map[string]any {
	return c.get(ctx, "/version", c.cfg.RequestTimeout).Body
}

func (c *Client) Algorithms(ctx context.Context, locale string, timeout time.Duration) Result {
	if len(locale) > maxLocaleChars {
		locale = locale[:maxLocaleChars]
	}
	return c.get(ctx, "/v1/algorithms?locale="+url.QueryEscape(locale), timeout)
}

func (c *Client) UploadHistory(ctx context.Context, body map[string]any, timeout time.Duration) Result {
	return c.post(ctx, "/v1/history", body, timeout)
}

func (c *Client) Plan(ctx context.Context, snapshot map[string]any, timeout time.Duration) Result {
	return c.post(ctx, "/v1/plan", snapshot, timeout)
}

func (c *Client) Rank(ctx context.Context, body map[string]any, timeout time.Duration) Result {
	return c.post(ctx, "/v1/rank", body, timeout)
}

func (c *Client) Profile(ctx context.Context, snapshot map[string]any, timeout time.Duration) Result {
	return c.post(ctx, "/v1/profile", snapshot, timeout)
}

func (c *Client) get(ctx context.Context, path string, timeout time.Duration) Result {
	return c.execute(ctx, http.MethodGet, path, nil, timeout)
}

func (c *Client) post(ctx context.Context, path string, body map[string]any, timeout time.Duration) Result {
	b, err := json.Marshal(body)
	if err != nil {
		return Result{Err: "encode"}
	}
	return c.execute(ctx, http.MethodPost, path, b, timeout)
}

func (c *Client) execute(ctx context.Context, method, path string, body []byte, timeout time.Duration) Result {
	if !c.cfg.Installed() {
		return Result{Err: "recommender not installed"}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.ServiceURL+path, rd)
	if err != nil {
		return Result{Err: "request"}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.InternalToken != "" {
		req.Header.Set("X-Internal-Token", c.cfg.InternalToken)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return Result{Err: "unreachable"}
	}
	defer res.Body.Close()
	text, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	switch {
	case err != nil:
		return Result{Err: "read"}
	case res.StatusCode == http.StatusConflict:
		return Result{Expired: true}
	case res.StatusCode < 200 || res.StatusCode > 299:
		return Result{Err: "HTTP " + strconv.Itoa(res.StatusCode)}
	case len(bytes.TrimSpace(text)) == 0:
		return Result{Body: map[string]any{}}
	}
	var out map[string]any
	if json.Unmarshal(text, &out) != nil || out == nil {
		return Result{Err: "invalid JSON"}
	}
	return Result{Body: out}
}
