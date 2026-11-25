package main

import (
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestHealthCheck(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(int(status.Load()))
	})}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("PORT", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	if healthCheck() != 0 {
		t.Fatal("a healthy server failed the check")
	}
	status.Store(http.StatusServiceUnavailable)
	if healthCheck() != 1 {
		t.Fatal("a 503 passed the check")
	}
}
