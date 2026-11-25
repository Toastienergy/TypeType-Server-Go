package httpx

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

type inner struct {
	A int `json:"a,omitempty"`
	B *string
}

type outer struct {
	inner
	Name    string            `json:"name"`
	Opt     *int              `json:"opt,omitempty"`
	List    []string          `json:"list"`
	Empty   []int             `json:"empty,omitempty"`
	Map     map[string]any    `json:"map"`
	Tags    map[string]string `json:"tags,omitempty"`
	Raw     json.RawMessage   `json:"raw"`
	Bytes   []byte            `json:"bytes"`
	When    time.Time         `json:"when"`
	Any     any               `json:"any"`
	Float   float64           `json:"float"`
	private int
}

func oldMarshal(t *testing.T, v any) []byte {
	b, err := marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJSONWritesWhatTheV1EncoderWrote(t *testing.T) {
	s, n := "x<y>&z\u2028", 3
	values := []any{
		outer{inner: inner{A: 1, B: &s}, Name: "a<b>&c \u2029 \xff end", Opt: &n, Map: map[string]any{"z": 1.5, "a": []any{nil, true, "q"}},
			Raw: json.RawMessage(`{"k": [1, 2]}`), Bytes: []byte{0, 1, 2, 255}, When: time.Date(2026, 11, 25, 8, 0, 0, 123, time.UTC),
			Any: map[string]int{"b": 2, "a": 1}, Float: 1e21},
		outer{}, []outer{{Float: 0.1}, {Float: -0}, {Float: math.MaxFloat64}, {Float: 1e-7}},
		map[string]any{"items": []any{}, "nextpage": nil, "available": true, "n": 12345678901234567890.0},
		nil, "plain", 42, []string(nil), map[string]string(nil), ErrorBody{Error: "e", Code: "c"},
	}
	if path := os.Getenv("TEST_NEXT_FIXTURE"); path != "" {
		raw, _ := os.ReadFile(path)
		var tree map[string]any
		json.Unmarshal(raw, &tree)
		values = append(values, tree)
	}
	for i, v := range values {
		w := httptest.NewRecorder()
		JSON(w, 200, v)
		if want := oldMarshal(t, v); !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatalf("value %d:\n%s\nwant\n%s", i, w.Body.Bytes()[:min(300, w.Body.Len())], want[:min(300, len(want))])
		}
	}
}
