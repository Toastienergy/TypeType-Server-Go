package main

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
)

// startProfiling serves the Go profiles on PPROF_ADDR, for an operator on the host only; unset, nothing listens.
func startProfiling(log *slog.Logger) {
	addr := os.Getenv("PPROF_ADDR")
	if addr == "" {
		return
	}
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(100_000)
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	go func() { log.Warn("pprof stopped", "err", http.ListenAndServe(addr, mux)) }()
}
