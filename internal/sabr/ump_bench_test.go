package sabr

import (
	"bufio"
	"bytes"
	"testing"
)

func umpPut(b *bytes.Buffer, v int) {
	switch {
	case v < 128:
		b.WriteByte(byte(v))
	case v < 1<<14:
		b.WriteByte(byte(0x80 | v&0x3f))
		b.WriteByte(byte(v >> 6))
	default:
		b.WriteByte(0xf0)
		b.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
	}
}

func umpPart(b *bytes.Buffer, typ int, payload []byte) {
	umpPut(b, typ)
	umpPut(b, len(payload))
	b.Write(payload)
}

// umpStream is one response: per segment a media header, eight 16 KiB media parts and an end, plus the control
// parts a real response carries around them.
func umpStream(segments int) ([]byte, int) {
	var b bytes.Buffer
	parts := 0
	chunk := make([]byte, 16<<10)
	for seq := 1; seq <= segments; seq++ {
		var h pw
		h.varint(1, uint64(seq%250))
		h.varint(3, 140)
		h.varint(9, uint64(seq))
		h.varint(11, uint64(seq*10000))
		h.varint(12, 10000)
		h.varint(14, uint64(8*len(chunk)))
		umpPart(&b, 20, h.b)
		for i := 0; i < 8; i++ {
			umpPart(&b, 21, append([]byte{byte(seq % 250)}, chunk...))
		}
		umpPart(&b, 22, []byte{byte(seq % 250)})
		var c pw
		c.varint(4, 0)
		umpPart(&b, 35, c.b)
		parts += 11
	}
	return b.Bytes(), parts
}

func BenchmarkReadUMP(b *testing.B) {
	stream, parts := umpStream(24)
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	br := bufio.NewReaderSize(bytes.NewReader(nil), 64<<10)
	for b.Loop() {
		s := &Session{changed: make(chan struct{}), info: &Info{}}
		s.tracks[audioTrack] = newTrack(&Format{Itag: 140})
		br.Reset(bytes.NewReader(stream))
		var cookie []byte
		var out roundResult
		if err := s.readUMP(br, s.info, &cookie, &out); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(parts), "parts/op")
}
