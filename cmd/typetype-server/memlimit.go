package main

import (
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"typetype.dev/server/internal/config"
)

// setMemoryLimit gives the garbage collector a soft limit when GOMEMLIMIT is not set. SABR media live in mappings
// outside the Go heap, under their own byte budget, so the limit covers the rest: 256 MB plus a quarter of the
// budget for the decode buffers that grow with playback, under nine tenths of a container memory limit minus the
// budget. Heap and media together stay where the single heap limit kept them before media left the heap.
func setMemoryLimit(log *slog.Logger) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	limit := heapLimit(int64(max(64, config.IntEnv("SABR_SESSION_BYTE_BUDGET_MB", 512)))<<20, cgroupLimit())
	debug.SetMemoryLimit(limit)
	log.Info("memory limit", "mb", limit>>20)
}

func heapLimit(budget, cgroup int64) int64 {
	limit := budget/4 + 256<<20
	if cgroup > 0 && cgroup*9/10-budget < limit {
		limit = max(64<<20, cgroup*9/10-budget)
	}
	return limit
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
