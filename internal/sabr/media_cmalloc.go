//go:build linux && cgo && cgo_perf

package sabr

/*
#include <stdlib.h>
*/
import "C"

import (
	"sync/atomic"
	"unsafe"
)

// The cgo_perf build keeps segment bytes in C malloc memory instead of anonymous mappings, for the A/B against
// media_linux.go; glibc serves these sizes from its own mappings or arenas.
const mappedMin = 64 << 10

var mappedBytes atomic.Int64

func mediaAlloc(n int) []byte {
	if n < mappedMin {
		return make([]byte, 0, n)
	}
	p := C.malloc(C.size_t(n))
	if p == nil {
		panic("sabr: out of memory for a media segment")
	}
	mappedBytes.Add(int64(n))
	return unsafe.Slice((*byte)(p), n)[:0]
}

func mediaFree(b []byte) {
	if cap(b) < mappedMin {
		return
	}
	C.free(unsafe.Pointer(unsafe.SliceData(b[:1])))
	mappedBytes.Add(-int64(cap(b)))
}

func trimSpare(int) {}
