package recommend

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
)

type Persona struct {
	Kind  int
	Quick int
	Deep  int
}

const (
	PersonaAuto = iota
	PersonaQuick
	PersonaDeep
)

// Cursor is HomeRecommendationCursor: where each list stopped, plus the diversity memory of the pages already shown.
type Cursor struct {
	SubIndex, DiscIndex, SubRun int
	PreferDiscovery             bool
	Seed                        int64
	RecentChannels              []string
	RecentSemantic              []string
	Momentum                    map[string]int
	Cooldown                    map[string]int64
	TopicPairs                  []string
	RecentURLs                  []string
	Persona                     Persona
}

func NewCursor() Cursor { return Cursor{PreferDiscovery: true} }

func (c Cursor) initial() bool {
	return c.SubIndex == 0 && c.DiscIndex == 0 && c.SubRun == 0 && len(c.RecentURLs) == 0
}

type payload struct {
	S  *int             `json:"s"`
	D  *int             `json:"d"`
	R  *int             `json:"r"`
	P  *int             `json:"p"`
	F  int64            `json:"f,omitempty"`
	C  []string         `json:"c,omitempty"`
	K  []string         `json:"k,omitempty"`
	M  map[string]int   `json:"m,omitempty"`
	O  map[string]int64 `json:"o,omitempty"`
	T  []string         `json:"t,omitempty"`
	U  []string         `json:"u,omitempty"`
	Q  int              `json:"q,omitempty"`
	QE int              `json:"qe,omitempty"`
	DE int              `json:"de,omitempty"`
}

var (
	multiCursor  = regexp.MustCompile(`^\{"s":(\d+),"d":(\d+)}$`)
	legacyCursor = regexp.MustCompile(`^\{"index":(\d+)}$`)
)

// DecodeCursor reads the compressed "z." cursor and the two older plain forms; ok is false for anything else.
func DecodeCursor(raw string) (Cursor, bool) {
	if strings.TrimSpace(raw) == "" {
		return NewCursor(), true
	}
	body, compressed := strings.CutPrefix(raw, "z.")
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(body, "="))
	if err != nil {
		return Cursor{}, false
	}
	if compressed {
		zr, err := zlib.NewReader(bytes.NewReader(b))
		if err != nil {
			return Cursor{}, false
		}
		b, err = io.ReadAll(io.LimitReader(zr, 32<<10+1))
		if err != nil || len(b) > 32<<10 {
			return Cursor{}, false
		}
	}
	var p payload
	if json.Unmarshal(b, &p) == nil && p.S != nil && p.D != nil && p.R != nil && p.P != nil {
		c := Cursor{SubIndex: *p.S, DiscIndex: *p.D, SubRun: *p.R, PreferDiscovery: *p.P == 1, Seed: p.F, RecentChannels: p.C,
			RecentSemantic: p.K, Momentum: p.M, Cooldown: p.O, TopicPairs: p.T, RecentURLs: p.U, Persona: Persona{Quick: p.QE, Deep: p.DE}}
		if p.Q == 1 || p.Q == 2 {
			c.Persona.Kind = p.Q
		}
		return c, true
	}
	if m := multiCursor.FindStringSubmatch(string(b)); m != nil {
		s, err1 := strconv.Atoi(m[1])
		d, err2 := strconv.Atoi(m[2])
		return Cursor{SubIndex: s, DiscIndex: d, PreferDiscovery: true}, err1 == nil && err2 == nil
	}
	if m := legacyCursor.FindStringSubmatch(string(b)); m != nil {
		i, err := strconv.Atoi(m[1])
		return Cursor{SubIndex: i, DiscIndex: i, PreferDiscovery: true}, err == nil
	}
	return Cursor{}, false
}

func (c Cursor) Encode() string {
	p := 0
	if c.PreferDiscovery {
		p = 1
	}
	b, _ := json.Marshal(payload{S: &c.SubIndex, D: &c.DiscIndex, R: &c.SubRun, P: &p, F: c.Seed, C: c.RecentChannels, K: c.RecentSemantic,
		M: c.Momentum, O: c.Cooldown, T: c.TopicPairs, U: c.RecentURLs, Q: c.Persona.Kind, QE: c.Persona.Quick, DE: c.Persona.Deep})
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return "z." + base64.RawURLEncoding.EncodeToString(buf.Bytes())
}
