//go:build linux

package sabr

import (
	"testing"
	"unsafe"
)

func spareBytes() int {
	spare.Lock()
	defer spare.Unlock()
	return spare.bytes
}

func TestSpareMappingsAreReusedWithinAQuarter(t *testing.T) {
	trimSpare(0)
	a := mediaAlloc(500 << 10)
	pa := unsafe.SliceData(a[:1])
	mediaFree(a)
	if spareBytes() != 500<<10 {
		t.Fatalf("%d spare bytes", spareBytes())
	}
	if b := mediaAlloc(300 << 10); unsafe.SliceData(b[:1]) == pa {
		t.Fatal("a 300 KB segment took a 500 KB mapping")
	} else {
		mediaFree(b)
	}
	c := mediaAlloc(450 << 10)
	if unsafe.SliceData(c[:1]) != pa || cap(c) != 500<<10 {
		t.Fatal("a 450 KB segment did not reuse the 500 KB mapping")
	}
	mediaFree(c)
	trimSpare(0)
	if spareBytes() != 0 || len(spare.mapped) != 0 {
		t.Fatalf("%d spare bytes, %d mappings known after trimming", spareBytes(), len(spare.mapped))
	}
}

func TestSpareListStaysUnderItsBound(t *testing.T) {
	trimSpare(0)
	var held [][]byte
	for range 40 {
		held = append(held, mediaAlloc(1<<20))
	}
	for _, b := range held {
		mediaFree(b)
	}
	if got := spareBytes(); got > spareMax {
		t.Fatalf("%d spare bytes", got)
	}
	if trimSpare(3); spareBytes() == 0 {
		t.Fatal("trimmed while sessions play and the list is fresh")
	}
	trimSpare(0)
}

func BenchmarkMediaAllocFree(b *testing.B) {
	for _, reuse := range []bool{false, true} {
		b.Run(map[bool]string{false: "fresh", true: "reused"}[reuse], func(b *testing.B) {
			trimSpare(0)
			for b.Loop() {
				m := mediaAlloc(900 << 10)
				mediaFree(m)
				if !reuse {
					trimSpare(0)
				}
			}
			trimSpare(0)
		})
	}
}

func TestFullSpareListDropsItsOldestMappings(t *testing.T) {
	trimSpare(0)
	held := make([][]byte, 40)
	for i := range held {
		held[i] = mediaAlloc(1 << 20)
	}
	first := unsafe.Pointer(unsafe.SliceData(held[0][:1]))
	for _, m := range held {
		mediaFree(m)
	}
	small := mediaAlloc(200 << 10)
	newest := unsafe.Pointer(unsafe.SliceData(small[:1]))
	mediaFree(small)
	spare.Lock()
	_, firstKept := spare.mapped[first]
	_, newestKept := spare.mapped[newest]
	spare.Unlock()
	if firstKept || !newestKept || spareBytes() > spareMax {
		t.Fatalf("oldest kept %v, newest kept %v, %d spare bytes", firstKept, newestKept, spareBytes())
	}
	trimSpare(0)
}
