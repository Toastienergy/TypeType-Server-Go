//go:build linux && !(cgo && cgo_perf)

package sabr

import (
	"os"
	"sync/atomic"
	"syscall"
)

// mappedMin keeps small buffers on the heap, where a page-rounded mapping would waste more than it saves.
const mappedMin = 64 << 10

var (
	pageSize    = os.Getpagesize()
	mappedBytes atomic.Int64
)

// mediaAlloc maps with MAP_POPULATE: the pump fills every byte, and faulting the pages in one call is cheaper.
func mediaAlloc(n int) []byte {
	if n < mappedMin {
		return make([]byte, 0, n)
	}
	size := (n + pageSize - 1) &^ (pageSize - 1)
	m, err := syscall.Mmap(-1, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE|syscall.MAP_POPULATE)
	if err != nil {
		return make([]byte, 0, n)
	}
	mappedBytes.Add(int64(size))
	return m[:0]
}

// mediaFree unmaps b; syscall.Munmap refuses slices it did not map, so heap fallbacks are left to the collector.
func mediaFree(b []byte) {
	if cap(b) < mappedMin {
		return
	}
	if syscall.Munmap(b[:cap(b)]) == nil {
		mappedBytes.Add(-int64(cap(b)))
	}
}
