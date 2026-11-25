package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"typetype.dev/server/internal/api"
	"typetype.dev/server/internal/cache"
	"typetype.dev/server/internal/config"
	"typetype.dev/server/internal/db"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	start := time.Now()
	startProfiling(log)
	setMemoryLimit(log)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dsn, err := cfg.PostgresDSN()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		return err
	}
	// Postgres and Dragonfly are dialed at the same time: each costs a few round trips and neither needs the other.
	var kv *cache.Cache
	var kvErr error
	kvDone := make(chan struct{})
	go func() {
		kv, kvErr = cache.Open(cfg.DragonflyURL)
		close(kvDone)
	}()
	dbStart := time.Now()
	pool, err := db.Open(ctx, dsn)
	dbMs := time.Since(dbStart).Milliseconds()
	<-kvDone
	if err != nil {
		if kvErr == nil {
			kv.Close()
		}
		return err
	}
	defer pool.Close()
	if kvErr != nil {
		return kvErr
	}
	defer kv.Close()
	srv := api.New(cfg, pool, kv, log)
	handler, rt := srv.Routes()
	written, total := rt.Written()
	hs := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 << 10, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	log.Info("ready", "port", cfg.Port, "startupMs", time.Since(start).Milliseconds(), "databaseMs", dbMs, "routes", written, "of", total)
	go srv.Admin.Get(ctx)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	srv.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return hs.Shutdown(shutdown)
}
