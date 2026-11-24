package sabr

import "encoding/binary"

// pw appends protobuf fields to a byte slice; requests are small and built once per round trip.
type pw struct{ b []byte }

func (w *pw) raw(v uint64) {
	for v >= 0x80 {
		w.b = append(w.b, byte(v)|0x80)
		v >>= 7
	}
	w.b = append(w.b, byte(v))
}

func (w *pw) varint(field int, v uint64) {
	w.raw(uint64(field) << 3)
	w.raw(v)
}

func (w *pw) bytes(field int, data []byte) {
	w.raw(uint64(field)<<3 | 2)
	w.raw(uint64(len(data)))
	w.b = append(w.b, data...)
}

func (w *pw) str(field int, s string) {
	if s != "" {
		w.raw(uint64(field)<<3 | 2)
		w.raw(uint64(len(s)))
		w.b = append(w.b, s...)
	}
}

func (w *pw) fixed32(field int, v uint32) {
	w.raw(uint64(field)<<3 | 5)
	w.b = binary.LittleEndian.AppendUint32(w.b, v)
}

func readVarint(data []byte, i int) (uint64, int, bool) {
	var v uint64
	for shift := uint(0); i < len(data) && shift < 64; shift += 7 {
		b := data[i]
		i++
		v |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return v, i, true
		}
	}
	return v, i, false
}

// fields walks a message without allocating; fn sees each field once, in wire order, and the last value of a
// repeated singular field wins for callers that keep overwriting.
func fields(data []byte, fn func(num int, v uint64, raw []byte)) {
	for i := 0; i < len(data); {
		key, n, ok := readVarint(data, i)
		if !ok {
			return
		}
		i = n
		num := int(key >> 3)
		switch key & 7 {
		case 0:
			v, n, ok := readVarint(data, i)
			if !ok {
				return
			}
			i = n
			fn(num, v, nil)
		case 1:
			if i+8 > len(data) {
				return
			}
			fn(num, binary.LittleEndian.Uint64(data[i:]), nil)
			i += 8
		case 2:
			l, n, ok := readVarint(data, i)
			if !ok || n+int(l) > len(data) || int(l) < 0 {
				return
			}
			fn(num, 0, data[n:n+int(l)])
			i = n + int(l)
		case 5:
			if i+4 > len(data) {
				return
			}
			fn(num, uint64(binary.LittleEndian.Uint32(data[i:])), nil)
			i += 4
		default:
			return
		}
	}
}

func varintField(data []byte, want int) uint64 {
	var out uint64
	fields(data, func(num int, v uint64, _ []byte) {
		if num == want {
			out = v
		}
	})
	return out
}

func bytesField(data []byte, want int) []byte {
	var out []byte
	fields(data, func(num int, _ uint64, raw []byte) {
		if num == want && raw != nil {
			out = raw
		}
	})
	return out
}
