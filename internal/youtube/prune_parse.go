package youtube

import (
	"bytes"
	"encoding/binary"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

var errSyntax = errors.New("youtube: invalid JSON in answer")

// bodies keeps a few answer buffers across GC cycles, which a sync.Pool would drop: a channel or watch answer is
// 0.5 to 1 MB, read again for every page of the subscription feed.
var bodies = make(chan *bytes.Buffer, 4)

// decodePruned decodes a JSON object as encoding/json does into map[string]any (numbers as float64), leaving out
// every member whose name is in skip, at any depth. A skipped member is passed over by matching its brackets
// outside strings, without parsing what is inside, which is most of the time spent on a channel or watch page.
func decodePruned(r io.Reader, skip map[string]bool) (map[string]any, error) {
	var buf *bytes.Buffer
	select {
	case buf = <-bodies:
		buf.Reset()
	default:
		buf = new(bytes.Buffer)
	}
	defer func() {
		if buf.Cap() <= 4<<20 {
			select {
			case bodies <- buf:
			default:
			}
		}
	}()
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	return decodePrunedBytes(buf.Bytes(), skip)
}

func decodePrunedBytes(b []byte, skip map[string]bool) (map[string]any, error) {
	p := &parser{b: b, skip: skip, names: map[string]string{}}
	p.space()
	if p.i >= len(b) || b[p.i] != '{' {
		return nil, errors.New("youtube: answer is not a JSON object")
	}
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	if p.space(); p.i != len(b) {
		return nil, errSyntax
	}
	return v.(map[string]any), nil
}

// parser reads one answer; names keeps each member name once, since the same few hundred names repeat thousands
// of times in a page.
type parser struct {
	b     []byte
	i     int
	skip  map[string]bool
	names map[string]string
}

const maxDepth = 10000

func (p *parser) space() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) value(depth int) (any, error) {
	if p.i >= len(p.b) || depth > maxDepth {
		return nil, errSyntax
	}
	switch c := p.b[p.i]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		raw, plain, err := p.str()
		if err != nil {
			return nil, err
		}
		if plain {
			return string(raw[1 : len(raw)-1]), nil
		}
		s, err := jsontext.AppendUnquote(nil, raw)
		return string(s), err
	case c == '-' || c >= '0' && c <= '9':
		return p.number()
	default:
		return p.literal()
	}
}

func (p *parser) object(depth int) (any, error) {
	p.i++
	m := map[string]any{}
	p.space()
	if p.i < len(p.b) && p.b[p.i] == '}' {
		p.i++
		return m, nil
	}
	for {
		if p.i >= len(p.b) || p.b[p.i] != '"' {
			return nil, errSyntax
		}
		raw, plain, err := p.str()
		if err != nil {
			return nil, err
		}
		name, ok := p.names[string(raw)]
		if !ok {
			if plain {
				name = string(raw[1 : len(raw)-1])
			} else if u, err := jsontext.AppendUnquote(nil, raw); err == nil {
				name = string(u)
			} else {
				return nil, err
			}
			p.names[string(raw)] = name
		}
		p.space()
		if p.i >= len(p.b) || p.b[p.i] != ':' {
			return nil, errSyntax
		}
		p.i++
		p.space()
		if p.skip[name] {
			if err := p.skipValue(); err != nil {
				return nil, err
			}
		} else {
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			m[name] = v
		}
		p.space()
		if p.i >= len(p.b) {
			return nil, errSyntax
		}
		switch p.b[p.i] {
		case ',':
			p.i++
			p.space()
		case '}':
			p.i++
			return m, nil
		default:
			return nil, errSyntax
		}
	}
}

func (p *parser) array(depth int) (any, error) {
	p.i++
	list := []any{}
	p.space()
	if p.i < len(p.b) && p.b[p.i] == ']' {
		p.i++
		return list, nil
	}
	for {
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
		p.space()
		if p.i >= len(p.b) {
			return nil, errSyntax
		}
		switch p.b[p.i] {
		case ',':
			p.i++
			p.space()
		case ']':
			p.i++
			return list, nil
		default:
			return nil, errSyntax
		}
	}
}

// str returns the quoted string at p.i, quotes included; plain reports a string without escapes, whose bytes
// between the quotes are the value itself.
func (p *parser) str() (raw []byte, plain bool, err error) {
	start := p.i
	if err := p.skipString(); err != nil {
		return nil, false, err
	}
	raw = p.b[start:p.i]
	body := raw[1 : len(raw)-1]
	if hasControl(body) || !utf8.Valid(body) {
		return nil, false, errSyntax
	}
	return raw, bytes.IndexByte(body, '\\') < 0, nil
}

// skipString moves past the string at p.i: IndexByte finds each quote with SIMD, and a quote after an odd run of
// backslashes is escaped.
func (p *parser) skipString() error {
	j := p.i + 1
	for {
		k := bytes.IndexByte(p.b[j:], '"')
		if k < 0 {
			return errSyntax
		}
		j += k
		n := 0
		for m := j - 1; m > p.i && p.b[m] == '\\'; m-- {
			n++
		}
		if n%2 == 0 {
			p.i = j + 1
			return nil
		}
		j++
	}
}

// hasControl reports a byte below 0x20, eight bytes at a time: (x - 0x20 in each byte) &^ x keeps a high bit only
// where a byte was below 0x20.
func hasControl(b []byte) bool {
	for len(b) >= 8 {
		x := binary.LittleEndian.Uint64(b)
		if (x-0x2020202020202020)&^x&0x8080808080808080 != 0 {
			return true
		}
		b = b[8:]
	}
	for _, c := range b {
		if c < 0x20 {
			return true
		}
	}
	return false
}

func (p *parser) number() (any, error) {
	start := p.i
	b := p.b
	i := p.i
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && b[i] >= '1' && b[i] <= '9':
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	default:
		return nil, errSyntax
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i >= len(b) || b[i] < '0' || b[i] > '9' {
			return nil, errSyntax
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= len(b) || b[i] < '0' || b[i] > '9' {
			return nil, errSyntax
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	p.i = i
	f, err := strconv.ParseFloat(string(b[start:i]), 64)
	if err != nil {
		return nil, errSyntax
	}
	return f, nil
}

func (p *parser) literal() (any, error) {
	rest := p.b[p.i:]
	switch {
	case bytes.HasPrefix(rest, []byte("true")):
		p.i += 4
		return true, nil
	case bytes.HasPrefix(rest, []byte("false")):
		p.i += 5
		return false, nil
	case bytes.HasPrefix(rest, []byte("null")):
		p.i += 4
		return nil, nil
	}
	return nil, errSyntax
}

// skipValue passes over one value by its brackets; strings are walked so brackets inside them do not count. What
// lies inside a skipped member is not checked further.
func (p *parser) skipValue() error {
	depth := 0
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case '"':
			if err := p.skipString(); err != nil {
				return err
			}
			if depth == 0 {
				return nil
			}
			continue
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return nil
			}
			depth--
			if depth == 0 {
				p.i++
				return nil
			}
		case ',':
			if depth == 0 {
				return nil
			}
		case ' ', '\t', '\n', '\r':
			if depth == 0 {
				return nil
			}
		}
		p.i++
	}
	if depth != 0 {
		return errSyntax
	}
	return nil
}
