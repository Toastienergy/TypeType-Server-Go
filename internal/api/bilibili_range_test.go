package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// rangeServer serves data with Range support; the first request is cut after cut bytes, and later requests serve
// changed bytes when change is set.
func rangeServer(t *testing.T, data []byte, cut int, change bool) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body := data
		if change && n > 1 {
			body = bytes.ToUpper(data)
		}
		m := byteRange.FindStringSubmatch(r.Header.Get("Range"))
		start, end := int64(0), int64(len(body)-1)
		if m != nil {
			start, _ = strconv.ParseInt(m[1], 10, 64)
			if m[2] != "" {
				end, _ = strconv.ParseInt(m[2], 10, 64)
			}
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.Itoa(len(body)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		part := body[start : end+1]
		if n == 1 && cut > 0 {
			w.Write(part[:cut])
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Write(part)
	}))
	return srv, &calls
}

func testData(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b)
	for i := range b {
		b[i] = 'a' + b[i]%26
	}
	return b
}

func TestRetryingBodyResumes(t *testing.T) {
	data := testData(300 << 10)
	srv, calls := rangeServer(t, data, 100<<10, false)
	defer srv.Close()
	s := &Server{proxyClient: srv.Client()}
	m, cerr := s.readBilibiliRange(context.Background(), srv.URL+"/v.m4s", nil, "bytes=1000-299999")
	if cerr != nil {
		t.Fatal(cerr.msg)
	}
	got, err := io.ReadAll(m.body)
	m.close()
	if err != nil || !bytes.Equal(got, data[1000:300000]) || calls.Load() != 2 {
		t.Fatalf("err %v, %d bytes, equal %v, calls %d", err, len(got), bytes.Equal(got, data[1000:300000]), calls.Load())
	}
}

func TestRetryingBodyRefusesChangedFile(t *testing.T) {
	data := testData(300 << 10)
	srv, _ := rangeServer(t, data, 100<<10, true)
	defer srv.Close()
	s := &Server{proxyClient: srv.Client()}
	m, cerr := s.readBilibiliRange(context.Background(), srv.URL+"/v.m4s", nil, "bytes=0-299999")
	if cerr != nil {
		t.Fatal(cerr.msg)
	}
	_, err := io.ReadAll(m.body)
	m.close()
	if !errors.Is(err, errChangedBody) {
		t.Fatalf("err %v", err)
	}
}

func TestHedgedOpenPicksFasterMirror(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Write([]byte("slow"))
	}))
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("fast")) }))
	defer slow.Close()
	defer fast.Close()
	s := &Server{proxyClient: &http.Client{}}
	began := time.Now()
	used, res, err := s.hedgedOpen(context.Background(), slow.URL, fast.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if used != fast.URL || string(b) != "fast" || time.Since(began) > time.Second {
		t.Fatalf("used %s body %s after %v", used, b, time.Since(began))
	}
}

func TestSubchunkedRange(t *testing.T) {
	data := testData(3_500_000)
	srv, calls := rangeServer(t, data, 0, false)
	defer srv.Close()
	for _, env := range []string{"BILIBILI_SUBCHUNK_PROXY", "BILIBILI_CONCURRENT_PREFETCH"} {
		t.Setenv(env, "true")
		s := &Server{proxyClient: srv.Client()}
		calls.Store(0)
		m, cerr := s.readBilibiliRange(context.Background(), srv.URL+"/v.m4s", nil, "bytes=100-")
		if cerr != nil {
			t.Fatal(cerr.msg)
		}
		got, err := io.ReadAll(m.body)
		m.close()
		if err != nil || !bytes.Equal(got, data[100:]) || m.crange != "bytes 100-3499999/3500000" || m.length != "3499900" || calls.Load() != 4 {
			t.Fatalf("%s: err %v, %d bytes, equal %v, %s, length %s, calls %d", env, err, len(got), bytes.Equal(got, data[100:]), m.crange, m.length, calls.Load())
		}
	}
}
