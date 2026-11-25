package main

import (
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"typetype.dev/server/internal/config"
)

// setMemoryLimit gives the garbage collector a soft limit when GOMEMLIMIT is not set. Almost all live memory is
// SABR media under its byte budget; without a limit the heap grows to about twice the live set before a cycle and
// keeps the freed pages, so ten 1080p sessions held 1.35 GB for 0.8 GB of live data. The limit is the budget plus a
// quarter and 256 MB for everything else, and stays under nine tenths of a container memory limit.
func setMemoryLimit(log *slog.Logger) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	budget := int64(max(64, config.IntEnv("SABR_SESSION_BYTE_BUDGET_MB", 512))) << 20
	limit := budget + budget/4 + 256<<20
	if c := cgroupLimit(); c > 0 && c*9/10 < limit {
		limit = c * 9 / 10
	}
	debug.SetMemoryLimit(limit)
	log.Info("memory limit", "mb", limit>>20)
}

func cgroupLimit() int64 {
	b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
