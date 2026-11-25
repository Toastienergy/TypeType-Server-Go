package youtube

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

type embedPage struct {
	calls atomic.Int32
	body  string
	delay time.Duration
}

func (e *embedPage) RoundTrip(r *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	time.Sleep(e.delay)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(e.body)), Header: http.Header{}}, nil
}

func TestVisitorFetchesTheEmbedPageOnceForConcurrentCallers(t *testing.T) {
	page := &embedPage{body: strings.Repeat("x", 40000) + `"VISITOR_DATA":"Cgt2aXNpdG9y"` + strings.Repeat("y", 100000),
		delay: 50 * time.Millisecond}
	c := NewClientWithHTTP(&http.Client{Transport: page})
	var wg sync.WaitGroup
	got := make([]string, 16)
	for i := range got {
		wg.Go(func() { got[i] = c.Visitor(context.Background()) })
	}
	wg.Wait()
	for _, v := range got {
		if v != "Cgt2aXNpdG9y" {
			t.Fatalf("visitor %q", v)
		}
	}
	if n := page.calls.Load(); n != 1 {
		t.Fatalf("%d embed fetches", n)
	}
}

func TestVisitorDoesNotRetryAFailedFetchAtOnce(t *testing.T) {
	page := &embedPage{body: "no visitor here"}
	c := NewClientWithHTTP(&http.Client{Transport: page})
	for range 3 {
		if v := c.Visitor(context.Background()); v != "" {
			t.Fatalf("visitor %q", v)
		}
	}
	if n := page.calls.Load(); n != 1 {
		t.Fatalf("%d embed fetches", n)
	}
}

func TestScanVisitorAcrossSmallReads(t *testing.T) {
	page := strings.Repeat("a", 70000) + `"VISITOR_DATA":"Cgt2aXNpdG9y"` + strings.Repeat("b", 1000)
	if v := scanVisitor(iotest.HalfReader(strings.NewReader(page))); v != "Cgt2aXNpdG9y" {
		t.Fatalf("visitor %q", v)
	}
	if v := scanVisitor(strings.NewReader("nothing")); v != "" {
		t.Fatalf("visitor %q", v)
	}
}
