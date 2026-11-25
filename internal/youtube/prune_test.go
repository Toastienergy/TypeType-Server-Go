package youtube

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
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

func nextFixture(tb testing.TB) []byte {
	path := os.Getenv("TEST_NEXT_FIXTURE")
	if path == "" {
		tb.Skip("TEST_NEXT_FIXTURE not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func TestPrunedNextMatchesFull(t *testing.T) {
	b := nextFixture(t)
	var full map[string]any
	if err := json.Unmarshal(b, &full); err != nil {
		t.Fatal(err)
	}
	pruned, err := decodePruned(bytes.NewReader(b), nextSkip)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a, p := &Watch{}, &Watch{}
	parseNext(a, full, now)
	parseNext(p, pruned, now)
	if len(a.Related) == 0 || !reflect.DeepEqual(a, p) {
		t.Fatalf("pruned next differs: %d related full, %d pruned", len(a.Related), len(p.Related))
	}
	if tok := commentsToken(full); tok == "" || tok != commentsToken(pruned) {
		t.Fatalf("comments token lost: %q", commentsToken(pruned))
	}
	t.Logf("%d related, %d chapters", len(a.Related), len(a.Chapters))
}

func BenchmarkNextFull(b *testing.B) {
	data := nextFixture(b)
	b.ReportAllocs()
	for b.Loop() {
		var m map[string]any
		json.Unmarshal(data, &m)
		parseNext(&Watch{}, m, time.Now())
	}
}

func BenchmarkNextPruned(b *testing.B) {
	data := nextFixture(b)
	b.ReportAllocs()
	for b.Loop() {
		m, _ := decodePruned(bytes.NewReader(data), nextSkip)
		parseNext(&Watch{}, m, time.Now())
	}
}
