package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
)

func TestFailAnswersLeftViewerWithoutError(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	s.fail(w, httptest.NewRequest("GET", "/settings", nil).WithContext(ctx), context.Canceled)
	if w.Code != clientClosedRequest {
		t.Fatalf("left viewer got %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.fail(w, httptest.NewRequest("GET", "/settings", nil), errors.New("boom"))
	if w.Code != 500 {
		t.Fatalf("real failure got %d", w.Code)
	}
}
