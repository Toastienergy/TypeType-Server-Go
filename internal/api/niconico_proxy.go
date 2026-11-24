package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/httpx"
)

// nicoVideoProxy is NicoVideoProxyService behind /proxy/nicovideo: a manifest comes back with every reference turned
// into a /media handle, any other URL is piped with the client's Range. Both send the domand bid cookie.
func (s *Server) nicoVideoProxy(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !q.Has("url") {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	raw := q.Get("url")
	var param *string
	if q.Has("domand_bid") {
		v := q.Get("domand_bid")
		param = &v
	}
	manifest := strings.Contains(strings.ToLower(raw), ".m3u8")
	target, bid := raw, param
	if manifest {
		target, _, _ = strings.Cut(raw, "#")
		bid = nicoBid(param, raw)
	}
	if _, msg := validateProxyURL(target); msg != "" {
		httpx.Error(w, r, http.StatusBadRequest, msg, "error")
		return
	}
	ctx := r.Context()
	if manifest {
		var cancel func()
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "NicoNico proxy fetch failed", "error")
		return
	}
	req.Header.Set("User-Agent", browserUA)
	if bid != nil {
		req.Header.Set("Cookie", "domand_bid="+*bid)
	}
	if rng := r.Header.Get("Range"); rng != "" && !manifest {
		req.Header.Set("Range", rng)
	}
	res, err := s.proxyClient.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "NicoNico proxy fetch failed", "error")
		}
		return
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "Upstream returned "+strconv.Itoa(res.StatusCode), "error")
		return
	}
	h := w.Header()
	if manifest {
		body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
		if err != nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "NicoNico proxy fetch failed", "error")
			return
		}
		h.Set("Content-Type", "application/vnd.apple.mpegurl")
		io.WriteString(w, s.rewriteProviderHLS(r.Context(), string(body), target, bid))
		return
	}
	ct := res.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Cache-Control"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	copyBody(w, res.Body)
}
