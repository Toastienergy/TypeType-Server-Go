package youtube

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func channelFixture(tb testing.TB) []byte {
	path := os.Getenv("TEST_CHANNEL_FIXTURE")
	if path == "" {
		tb.Skip("TEST_CHANNEL_FIXTURE not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func TestPrunedChannelMatchesFull(t *testing.T) {
	b := channelFixture(t)
	var full map[string]any
	if err := json.Unmarshal(b, &full); err != nil {
		t.Fatal(err)
	}
	pruned, err := decodePruned(bytes.NewReader(b), channelSkip)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{}
	a, p := c.channelPage("UC", full), c.channelPage("UC", pruned)
	if len(a.Videos) == 0 || !reflect.DeepEqual(a, p) {
		t.Fatalf("pruned channel differs: %d videos full, %d pruned", len(a.Videos), len(p.Videos))
	}
	t.Logf("%d videos, %s", len(a.Videos), a.Name)
}

func TestDecodePrunedMatchesEncodingJSON(t *testing.T) {
	src := `{"a":1.5,"b":[true,false,null,"x",{"skip":{"deep":1},"keep":[]}],"skip":[1,2],"e":{}}`
	var want map[string]any
	json.Unmarshal([]byte(src), &want)
	delete(want, "skip")
	delete(want["b"].([]any)[4].(map[string]any), "skip")
	got, err := decodePruned(bytes.NewReader([]byte(src)), map[string]bool{"skip": true})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v %v", got, err)
	}
}

func BenchmarkChannelFull(b *testing.B) {
	data := channelFixture(b)
	c := &Client{}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		var m map[string]any
		json.Unmarshal(data, &m)
		c.channelPage("UC", m)
	}
}

func BenchmarkChannelPruned(b *testing.B) {
	data := channelFixture(b)
	c := &Client{}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		m, _ := decodePruned(bytes.NewReader(data), channelSkip)
		c.channelPage("UC", m)
	}
}
