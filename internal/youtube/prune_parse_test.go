package youtube

import (
	"encoding/json"
	"reflect"
	"testing"
	"unicode/utf8"
)

func TestParserMatchesEncodingJSONOnWholeAnswers(t *testing.T) {
	for _, b := range [][]byte{channelFixture(t), nextFixture(t)} {
		var want map[string]any
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatal(err)
		}
		got, err := decodePrunedBytes(b, nil)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("whole answer differs: %v", err)
		}
	}
}

func TestParserEdgeCases(t *testing.T) {
	for _, src := range []string{
		`{"ab":"é\n\"q\"","n":-0.5e+3,"big":12345678901234567890,"u":"😀","t":[[],{},[{}]]}`,
		` {"dup":1,"dup":2} `, `{"s":"café au lait","r":"日本語"}`,
	} {
		var want map[string]any
		if err := json.Unmarshal([]byte(src), &want); err != nil {
			t.Fatal(err)
		}
		got, err := decodePrunedBytes([]byte(src), nil)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: %v %v", src, got, err)
		}
	}
	for _, bad := range []string{`{"a":1`, `{"a":01}`, `{"a":1.}`, `{"a":tru}`, `[1]`, `{"a":1} x`, "{\"a\":\"\x01\"}",
		`{"a":"\x"}`, `{"a" 1}`, `{"a":[1,]}`, `{"s":{"a":[}`} {
		if _, err := decodePrunedBytes([]byte(bad), map[string]bool{"s": true}); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func FuzzParserAgainstEncodingJSON(f *testing.F) {
	for _, s := range []string{`{"a":[1,"x",{"b":null}],"c":true}`, `{"k":"é\\"}`, `{}`, `{"n":-1.5e-7}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := decodePrunedBytes(b, nil)
		var want map[string]any
		werr := json.Unmarshal(b, &want)
		switch {
		case err == nil && werr != nil:
			t.Fatalf("accepted what encoding/json rejects: %v", werr)
		case err != nil && werr == nil && want != nil && utf8.Valid(b):
			t.Fatalf("rejected valid JSON: %v", err)
		case err == nil && !reflect.DeepEqual(got, want):
			t.Fatalf("decoded %v, encoding/json %v", got, want)
		}
	})
}
