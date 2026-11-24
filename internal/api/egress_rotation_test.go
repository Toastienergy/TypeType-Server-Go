package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeGluetun struct {
	mu    sync.Mutex
	calls []string
	known map[string]bool
}

func (g *fakeGluetun) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Header.Get("X-API-Key") != "k" {
		w.WriteHeader(401)
		return
	}
	g.calls = append(g.calls, r.URL.Path+" "+string(b))
	if strings.Contains(string(b), `"names"`) {
		for n := range g.known {
			if strings.Contains(string(b), `"`+n+`"`) {
				return
			}
		}
		w.WriteHeader(400)
	}
}

func testRotation(t *testing.T, servers []string, known map[string]bool, accept func(string) bool) (*egressRotation, *fakeGluetun) {
	g := &fakeGluetun{known: known}
	srv := httptest.NewServer(http.HandlerFunc(g.handler))
	t.Cleanup(srv.Close)
	e := &egressRotation{s: &Server{Log: slog.New(slog.DiscardHandler)}, url: srv.URL, key: "k", servers: servers,
		refused: map[string]time.Time{}, unknown: map[string]bool{}, forget: func() {}}
	e.accept = func(_ context.Context, _ string) bool { return accept(e.current) }
	return e, g
}

func TestRotationSkipsUnknownAndKeepsAccepted(t *testing.T) {
	e, g := testRotation(t, []string{"gone"}, map[string]bool{}, func(string) bool { return true })
	if !e.rotate(context.Background(), "test", "v") || !e.unknown["gone"] || e.current != "" || len(g.calls) != 3 {
		t.Fatalf("current %q unknown %v calls %v", e.current, e.unknown, g.calls)
	}
	if e.rotate(context.Background(), "again", "v") {
		t.Fatal("rotated twice within ten minutes")
	}
}

func TestRotationReturnsToLastAccepted(t *testing.T) {
	e, g := testRotation(t, []string{"a", "b", "c"}, map[string]bool{"a": true, "b": true, "c": true}, func(string) bool { return false })
	e.accepted, e.current = "a", "c"
	if e.rotate(context.Background(), "test", "v") {
		t.Fatal("no exit accepted")
	}
	if e.current != "a" || !strings.Contains(g.calls[len(g.calls)-1], `"a"`) {
		t.Fatalf("did not go back: %q %v", e.current, g.calls)
	}
}

func TestRotationRandomReconnectWithoutNames(t *testing.T) {
	e, g := testRotation(t, nil, nil, func(string) bool { return true })
	if !e.rotate(context.Background(), "test", "v") || len(g.calls) != 2 || !strings.Contains(g.calls[0], "stopped") {
		t.Fatalf("calls %v", g.calls)
	}
}
