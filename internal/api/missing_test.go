package api

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"typetype.dev/server/internal/config"
)

func TestListMissingRoutes(t *testing.T) {
	if os.Getenv("TYPETYPE_LIST_MISSING") == "" {
		t.Skip("set TYPETYPE_LIST_MISSING to list the routes left to write")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, rt := New(cfg, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
	for _, m := range rt.Missing() {
		t.Log(m)
	}
}
