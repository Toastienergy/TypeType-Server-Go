//go:build linux

package sabr

import (
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// mappedMin keeps small buffers on the heap, where a page-rounded mapping would waste more than it saves.
const mappedMin = 64 << 10

var (
	pageSize = os.Getpagesize()
	// mappedBytes counts the mapped bytes segments hold; spare mappings are not counted.
	mappedBytes atomic.Int64
)

// spare keeps mappings released by segments for the next segments of about the same size. A fresh mapping costs a
// system call and the kernel zeroing every page: 11% of the server's CPU at twenty playbacks before this list. The
// pump overwrites every byte, so a reused mapping needs neither.
var spare struct {
	sync.Mutex
	maps   [][]byte
	bytes  int
	used   time.Time
	mapped map[unsafe.Pointer]struct{}
}

const (
	spareMax  = 32 << 20
	spareIdle = 30 * time.Second
)

// mediaAlloc takes the smallest spare mapping at most a quarter larger than asked, else maps with MAP_POPULATE:
// the pump fills every byte, and faulting the pages in one call is cheaper.
func mediaAlloc(n int) []byte {
	if n < mappedMin {
		return make([]byte, 0, n)
	}
	size := (n + pageSize - 1) &^ (pageSize - 1)
	spare.Lock()
	best := -1
	for i, m := range spare.maps {
		if c := cap(m); c >= size && c <= size+size/4 && (best < 0 || c < cap(spare.maps[best])) {
			best = i
		}
	}
	if best >= 0 {
		m := spare.maps[best]
		last := len(spare.maps) - 1
		spare.maps[best], spare.maps[last] = spare.maps[last], nil
		spare.maps = spare.maps[:last]
		spare.bytes -= cap(m)
		spare.used = time.Now()
		spare.Unlock()
		mappedBytes.Add(int64(cap(m)))
		return m[:0]
	}
	spare.Unlock()
	m, err := syscall.Mmap(-1, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE|syscall.MAP_POPULATE)
	if err != nil {
		return make([]byte, 0, n)
	}
	spare.Lock()
	if spare.mapped == nil {
		spare.mapped = map[unsafe.Pointer]struct{}{}
	}
	spare.mapped[unsafe.Pointer(unsafe.SliceData(m))] = struct{}{}
	spare.Unlock()
	mappedBytes.Add(int64(size))
	return m[:0]
}

// mediaFree keeps b for reuse while the spare list has room, and unmaps it otherwise; heap fallbacks are left to
// the collector.
func mediaFree(b []byte) {
	if cap(b) < mappedMin {
		return
	}
	b = b[:cap(b)]
	p := unsafe.Pointer(unsafe.SliceData(b))
	spare.Lock()
	if _, ok := spare.mapped[p]; !ok {
		spare.Unlock()
		return
	}
	mappedBytes.Add(-int64(len(b)))
	if spare.bytes+len(b) <= spareMax {
		spare.maps = append(spare.maps, b)
		spare.bytes += len(b)
		spare.used = time.Now()
		spare.Unlock()
		return
	}
	delete(spare.mapped, p)
	spare.Unlock()
	syscall.Munmap(b)
}

// trimSpare unmaps every spare mapping when no session plays or the list was left alone for spareIdle, so an idle
// server gives the memory back.
func trimSpare(sessions int) {
	spare.Lock()
	if len(spare.maps) == 0 || sessions > 0 && time.Since(spare.used) < spareIdle {
		spare.Unlock()
		return
	}
	maps := spare.maps
	spare.maps, spare.bytes = nil, 0
	for _, m := range maps {
		delete(spare.mapped, unsafe.Pointer(unsafe.SliceData(m)))
	}
	spare.Unlock()
	for _, m := range maps {
		syscall.Munmap(m)
	}
}
