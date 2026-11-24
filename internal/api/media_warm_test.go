package api

import (
	"encoding/binary"
	"slices"
	"testing"
)

func sidxBox(version byte, first uint64, sizes ...uint32) []byte {
	var b []byte
	b = append(b, 0, 0, 0, 0)
	b = append(b, "sidx"...)
	b = append(b, version, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0x3e, 0x80)
	if version == 0 {
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, uint32(first))
	} else {
		b = binary.BigEndian.AppendUint64(b, 0)
		b = binary.BigEndian.AppendUint64(b, first)
	}
	b = append(b, 0, 0)
	b = binary.BigEndian.AppendUint16(b, uint16(len(sizes)))
	for _, sz := range sizes {
		b = binary.BigEndian.AppendUint32(b, sz)
		b = append(b, 0, 0, 0, 0, 0x90, 0, 0, 0)
	}
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	return b
}

func TestSidxSegments(t *testing.T) {
	head := make([]byte, 100)
	box := sidxBox(0, 10, 500, 700)
	h := append(head, box...)
	got := sidxSegments(h, 100)
	end := int64(100 + len(box) + 10)
	if !slices.Equal(got, [][2]int64{{end, end + 499}, {end + 500, end + 1199}}) {
		t.Fatalf("v0 %v", got)
	}
	if g := sidxSegments(append(head, sidxBox(1, 0, 42)...), 100); len(g) != 1 || g[0][1]-g[0][0] != 41 {
		t.Fatalf("v1 %v", g)
	}
	if sidxSegments(append(head, sidxBox(0, 0, 0x80000001)...), 100) != nil || sidxSegments(h, 99) != nil {
		t.Fatal("bad box accepted")
	}
}

func TestStartCacheRanges(t *testing.T) {
	c := newStartCache()
	c.put("u", &startEntry{bytes: []byte("0123456789"), total: 100, ctype: "video/mp4"})
	if m := c.respond("u", "bytes=2-5"); m == nil || string(m.text) != "2345" || m.crange != "bytes 2-5/100" || m.status != 206 {
		t.Fatalf("range %+v", m)
	}
	if c.respond("u", "bytes=2-") != nil || c.respond("u", "") != nil || c.respond("u", "bytes=8-12") != nil {
		t.Fatal("partial entry answered past its bytes")
	}
	c.put(segmentKey("u", 50), &startEntry{bytes: []byte("abcde"), total: 100, offset: 50})
	if m := c.respond("u", "bytes=50-54"); m == nil || string(m.text) != "abcde" {
		t.Fatal("segment entry")
	}
	c.put("w", &startEntry{bytes: []byte("whole"), total: 5, complete: true})
	if m := c.respond("w", ""); m == nil || m.status != 200 || m.length != "5" {
		t.Fatal("complete entry")
	}
}

func TestHLSChain(t *testing.T) {
	h := func(n string) string { return "m1_" + n + "AAAAAAAAAAAAAAAAAAAAAAA"[:24-len(n)] }
	v := planHLS("#EXTM3U\n#EXT-X-MAP:URI=\"../media/" + h("vmap") + "\"\n#EXTINF:6.0,\n../media/" + h("v1") + "\n#EXTINF:6.0,\n../media/" + h("v2") + "\n")
	a := planHLS("#EXTINF:4.0,\n../media/" + h("a1") + "\n#EXTINF:4.0,\n../media/" + h("a2") + "\n#EXTINF:4.0,\n../media/" + h("a3") + "\n")
	if len(v.headers) != 1 || len(v.segments) != 2 || v.segments[1].startMs != 6000 {
		t.Fatalf("plan %+v", v)
	}
	c := newHLSChain()
	c.remember([][]hlsSeg{v.segments}, a.segments)
	if got := c.following(h("v2")); !slices.Equal(got, []string{h("a2")}) {
		t.Fatalf("v2 -> %v", got)
	}
	if got := c.following(h("v1")); !slices.Equal(got, []string{h("v2"), h("a1")}) {
		t.Fatalf("v1 -> %v", got)
	}
	if got := c.following(h("a1")); !slices.Equal(got, []string{h("a2")}) {
		t.Fatalf("a1 -> %v", got)
	}
}
