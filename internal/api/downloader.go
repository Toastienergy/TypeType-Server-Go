package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
)

// downloader forwards /downloader/* to the download service as DownloaderGatewayRoutes does: plain calls are
// buffered, event streams and artifacts are streamed, and an artifact stored behind the internal object store is
// fetched here so the client never sees that host.
type downloader struct {
	base   string
	client *http.Client
	stream *http.Client
}

func newDownloader(base string) *downloader {
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &downloader{base: strings.TrimRight(base, "/"), client: &http.Client{CheckRedirect: noRedirect, Timeout: 60 * time.Second},
		stream: &http.Client{CheckRedirect: noRedirect}}
}

var hopHeaders = map[string]bool{"content-length": true, "transfer-encoding": true, "connection": true}

var forcedHeaders = map[string]bool{"content-type": true, "content-disposition": true, "x-content-type-options": true,
	"cache-control": true, "pragma": true, "expires": true}

var leadingInline = regexp.MustCompile(`(?i)^inline`)

func (d *downloader) open(r *http.Request, c *http.Client, path string, body []byte) (*http.Response, error) {
	u := d.base + path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u, rd)
	if err != nil {
		return nil, err
	}
	for k, vs := range r.Header {
		if l := strings.ToLower(k); l == "host" || l == "content-length" || l == "connection" {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if id := httpx.RequestID(r.Context()); id != "" {
		req.Header.Set("X-Request-ID", id)
	}
	return c.Do(req)
}

func forceDownload(path string, q url.Values) bool {
	if !strings.HasSuffix(path, "/artifact") {
		return false
	}
	if !q.Has("download") {
		return true
	}
	v := strings.ToLower(q.Get("download"))
	return v != "0" && v != "false" && v != "no"
}

func copyHeaders(w http.ResponseWriter, h http.Header, force, keepLength bool) {
	for k, vs := range h {
		l := strings.ToLower(k)
		if (hopHeaders[l] && !(keepLength && l == "content-length")) || (force && forcedHeaders[l]) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

func applyDownloadHeaders(w http.ResponseWriter, upstream http.Header) {
	disp := upstream.Get("Content-Disposition")
	switch {
	case disp == "":
		disp = "attachment"
	case !strings.HasPrefix(strings.ToLower(disp), "attachment"):
		disp = leadingInline.ReplaceAllString(disp, "attachment")
	}
	w.Header().Set("Content-Disposition", disp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "application/octet-stream")
}

func pump(w http.ResponseWriter, body io.Reader, flush bool) {
	f, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flush && f != nil {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

var storageDetailNames = []string{"availableBytes", "dataDir", "diskFreeBytes", "diskTotalBytes", "freeBytes", "freePercent",
	"minFreeBytes", "requiredFreeBytes", "thresholdBytes", "totalBytes"}

func (s *Server) storageRefusal(w http.ResponseWriter, r *http.Request, body []byte) {
	var root map[string]any
	attrs := []any{"requestId", httpx.RequestID(r.Context())}
	if json.Unmarshal(body, &root) == nil {
		seen := map[string]bool{}
		for _, o := range []any{root, root["details"], root["disk"], root["storage"]} {
			m, _ := o.(map[string]any)
			for _, k := range storageDetailNames {
				if v, ok := m[k]; ok && !seen[k] {
					if _, nested := v.(map[string]any); !nested {
						seen[k] = true
						attrs = append(attrs, k, v)
					}
				}
			}
		}
	}
	s.Log.Warn("downloader storage refusal", attrs...)
	httpx.Error(w, r, http.StatusInsufficientStorage, "Stockage temporairement sature, reessayez plus tard.", "insufficient_storage")
}

func (s *Server) downloaderRoutes(rt *Router) {
	h := func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/downloader")
		if path == "" {
			path = "/"
		}
		var body []byte
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" {
			body, _ = io.ReadAll(r.Body)
		}
		if strings.HasSuffix(path, "/events") || strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
			res, err := s.dl.open(r, s.dl.stream, path, body)
			if err != nil {
				plain(w, r, http.StatusBadGateway, "downloader unavailable")
				return
			}
			defer res.Body.Close()
			copyHeaders(w, res.Header, false, false)
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "text/event-stream")
			}
			w.WriteHeader(res.StatusCode)
			pump(w, res.Body, true)
			return
		}
		force := forceDownload(path, r.URL.Query())
		if strings.HasSuffix(path, "/artifact") {
			s.downloaderArtifact(w, r, path, force)
			return
		}
		res, err := s.dl.open(r, s.dl.client, path, body)
		if err != nil {
			plain(w, r, http.StatusBadGateway, "downloader unavailable")
			return
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		if res.StatusCode == http.StatusInsufficientStorage {
			s.storageRefusal(w, r, out)
			return
		}
		copyHeaders(w, res.Header, force, false)
		if force {
			applyDownloadHeaders(w, res.Header)
		} else if res.Header.Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.WriteHeader(res.StatusCode)
		w.Write(out)
	}
	for _, m := range []string{"HEAD", "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		rt.Handle(m, "/downloader", h)
		rt.Handle(m, "/downloader/{...}", h)
	}
}

func internalArtifact(res *http.Response) bool {
	if res.StatusCode != http.StatusFound && res.StatusCode != http.StatusTemporaryRedirect {
		return false
	}
	loc := res.Header.Get("Location")
	if loc == "" {
		return false
	}
	if res.Header.Get("X-TypeType-Artifact-Proxy") == "1" {
		return true
	}
	u, err := url.Parse(loc)
	return err == nil && strings.EqualFold(u.Hostname(), "garage")
}

func (s *Server) downloaderArtifact(w http.ResponseWriter, r *http.Request, path string, force bool) {
	unavailable := func() { plain(w, r, http.StatusBadGateway, "artifact unavailable") }
	res, err := s.dl.open(r, s.dl.stream, path, nil)
	if err != nil {
		unavailable()
		return
	}
	if internalArtifact(res) {
		loc := res.Header.Get("Location")
		res.Body.Close()
		req, err := http.NewRequestWithContext(r.Context(), r.Method, loc, nil)
		if err != nil {
			unavailable()
			return
		}
		if rg := r.Header.Get("Range"); strings.TrimSpace(rg) != "" {
			req.Header.Set("Range", rg)
		}
		if res, err = s.dl.stream.Do(req); err != nil {
			unavailable()
			return
		}
	}
	defer res.Body.Close()
	copyHeaders(w, res.Header, force, true)
	if force {
		applyDownloadHeaders(w, res.Header)
	} else if res.Header.Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.WriteHeader(res.StatusCode)
	if r.Method != "HEAD" {
		pump(w, res.Body, false)
	}
}
