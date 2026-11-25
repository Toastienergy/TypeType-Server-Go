package sabr

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const hedgeTestURL = "https://rr3---sn-main.googlevideo.com/videoplayback?mn=sn-main%2Csn-spare&fvip=5&sig=x"

type hostTransport struct {
	mu    sync.Mutex
	hosts []string
	reply map[string]func(*http.Request) (*http.Response, error)
}

func (h *hostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.hosts = append(h.hosts, r.URL.Host)
	h.mu.Unlock()
	return h.reply[r.URL.Host](r)
}

func (h *hostTransport) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.hosts...)
}

func answer(status int, body string, wait time.Duration) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		select {
		case <-time.After(wait):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
}

func hedgeSession(t *testing.T, main, spare func(*http.Request) (*http.Response, error)) (*Session, *hostTransport) {
	t.Helper()
	old := hedgeAfter
	hedgeAfter = 30 * time.Millisecond
	t.Cleanup(func() { hedgeAfter = old })
	tr := &hostTransport{reply: map[string]func(*http.Request) (*http.Response, error){
		"rr3---sn-main.googlevideo.com": main, "rr5---sn-spare.googlevideo.com": spare}}
	return &Session{store: &Store{media: &http.Client{Transport: tr}}}, tr
}

func noTrace(c context.Context) context.Context { return c }

func TestFallbackURL(t *testing.T) {
	got := fallbackURL(hedgeTestURL)
	want := "https://rr5---sn-spare.googlevideo.com/videoplayback?mn=sn-main%2Csn-spare&fvip=5&sig=x&fallback_count=1"
	if got != want {
		t.Fatalf("fallbackURL = %q, want %q", got, want)
	}
	for _, raw := range []string{
		"https://rr3---sn-main.googlevideo.com/videoplayback?mn=sn-main&fvip=5",
		"https://rr3---sn-main.googlevideo.com/videoplayback?mn=sn-main,sn-spare",
		want,
	} {
		if got := fallbackURL(raw); got != "" {
			t.Errorf("fallbackURL(%q) = %q, want none", raw, got)
		}
	}
}

func TestSendTakesFallbackWhenMainIsSlow(t *testing.T) {
	s, tr := hedgeSession(t, answer(200, "main", 2*time.Second), answer(200, "spare", 0))
	began := time.Now()
	r := s.send(context.Background(), &Info{BaseURL: hedgeTestURL}, "rn=0", nil, true, noTrace)
	defer r.cancel()
	if !r.ok() || !r.fallback {
		t.Fatalf("want the fallback answer, got ok=%v fallback=%v err=%v", r.ok(), r.fallback, r.err)
	}
	if body, _ := io.ReadAll(r.resp.Body); string(body) != "spare" {
		t.Fatalf("body = %q", body)
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("took %v, the slow server was waited for", took)
	}
	if !strings.Contains(r.base, "fallback_count=1") {
		t.Fatalf("winner base %q is not marked as a fallback", r.base)
	}
	if hosts := tr.seen(); len(hosts) != 2 {
		t.Fatalf("hosts = %v", hosts)
	}
}

func TestSendDoesNotHedgeAFastAnswer(t *testing.T) {
	s, tr := hedgeSession(t, answer(200, "main", 0), answer(200, "spare", 0))
	r := s.send(context.Background(), &Info{BaseURL: hedgeTestURL}, "rn=0", nil, true, noTrace)
	defer r.cancel()
	time.Sleep(60 * time.Millisecond)
	if !r.ok() || r.fallback {
		t.Fatalf("want the main answer, got ok=%v fallback=%v", r.ok(), r.fallback)
	}
	if hosts := tr.seen(); len(hosts) != 1 {
		t.Fatalf("hosts = %v, the fallback was asked for nothing", hosts)
	}
}

func TestSendReturnsAnImmediateRefusalWithoutFallback(t *testing.T) {
	s, tr := hedgeSession(t, answer(403, "refused", 0), answer(200, "spare", 0))
	r := s.send(context.Background(), &Info{BaseURL: hedgeTestURL}, "rn=0", nil, true, noTrace)
	defer r.cancel()
	if r.err != nil || r.resp.StatusCode != 403 || r.fallback {
		t.Fatalf("want the main 403, got err=%v fallback=%v", r.err, r.fallback)
	}
	r.resp.Body.Close()
	time.Sleep(60 * time.Millisecond)
	if hosts := tr.seen(); len(hosts) != 1 {
		t.Fatalf("hosts = %v", hosts)
	}
}

func TestSendKeepsMainWhenFallbackFails(t *testing.T) {
	s, _ := hedgeSession(t, answer(200, "main", 120*time.Millisecond), answer(503, "", 0))
	r := s.send(context.Background(), &Info{BaseURL: hedgeTestURL}, "rn=0", nil, true, noTrace)
	defer r.cancel()
	if !r.ok() || r.fallback {
		t.Fatalf("want the main answer after the fallback failed, got ok=%v fallback=%v", r.ok(), r.fallback)
	}
}

func TestSendWithoutHedgeNeverAsksTheFallback(t *testing.T) {
	s, tr := hedgeSession(t, answer(200, "main", 100*time.Millisecond), answer(200, "spare", 0))
	r := s.send(context.Background(), &Info{BaseURL: hedgeTestURL}, "rn=5", nil, false, noTrace)
	defer r.cancel()
	if !r.ok() || r.fallback || len(tr.seen()) != 1 {
		t.Fatalf("want only the main server, got fallback=%v hosts=%v", r.fallback, tr.seen())
	}
}
