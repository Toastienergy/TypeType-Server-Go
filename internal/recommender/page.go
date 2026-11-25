package recommender

import (
	"context"
	"sort"
	"time"

	"typetype.dev/server/internal/youtube"
)

// Row is HomeRecommendationRow: a titled row of the home page and why it is there.
type Row struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Reason    string   `json:"reason"`
	ReasonRef *string  `json:"reasonRef"`
	VideoURLs []string `json:"videoUrls"`
}

// Page is RecommenderPage, kept in Dragonfly for PageTTL.
type Page struct {
	Algorithm string              `json:"algorithm"`
	Rows      []Row               `json:"rows"`
	Items     []youtube.VideoItem `json:"items"`
	Trace     *Trace              `json:"trace"`
}

// Trace is RecommenderPageTrace, shown on the last page view.
type Trace struct {
	Algorithm          string         `json:"algorithm"`
	BuiltAt            int64          `json:"builtAt"`
	Sources            []TraceSource  `json:"sources"`
	CandidatesReceived int            `json:"candidatesReceived"`
	Dropped            map[string]int `json:"dropped"`
	Shown              int            `json:"shown"`
	Rows               []TraceRow     `json:"rows"`
	Scores             []TraceScore   `json:"scores"`
}

type TraceSource struct {
	Kind     string `json:"kind"`
	Detail   string `json:"detail"`
	Received int    `json:"received"`
}

type TraceRow struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Why    string `json:"why"`
	Videos int    `json:"videos"`
}

type TraceScore struct {
	URL     string             `json:"url"`
	Title   string             `json:"title"`
	Channel string             `json:"channel"`
	Row     string             `json:"row"`
	Total   float64            `json:"total"`
	Factors map[string]float64 `json:"factors"`
}

// Builder is RecommenderPageBuilder: plan, load the planned sources, rank, keep the rows whose videos were loaded.
type Builder struct {
	client    *Client
	snapshots *Snapshots
	sources   *Sources
	metrics   *Metrics
}

func NewBuilder(client *Client, snapshots *Snapshots, sources *Sources, metrics *Metrics) *Builder {
	return &Builder{client: client, snapshots: snapshots, sources: sources, metrics: metrics}
}

func (b *Builder) Build(ctx context.Context, req PageRequest) *Page {
	snapshot := b.snapshots.Snapshot(ctx, req, false)
	if snapshot == nil {
		return nil
	}
	planStarted := time.Now()
	plan := b.client.Plan(ctx, snapshot, req.Timeout)
	if plan.Expired {
		if snapshot = b.snapshots.Snapshot(ctx, req, true); snapshot == nil {
			return nil
		}
		plan = b.client.Plan(ctx, snapshot, req.Timeout)
	}
	if !plan.OK() {
		return nil
	}
	rawSources, ok := plan.Body["sources"].([]any)
	if !ok {
		return nil
	}
	var sources []map[string]any
	for _, s := range rawSources {
		if o, ok := s.(map[string]any); ok {
			sources = append(sources, o)
		}
	}
	sourcesStarted := time.Now()
	candidates := b.sources.Fetch(ctx, req.User, sources, req.Seed)
	rankStarted := time.Now()
	ranked := b.rank(ctx, plan.Body, snapshot, candidates, req.Timeout)
	if ranked == nil {
		return nil
	}
	b.metrics.SourceMillis.Add(rankStarted.Sub(sourcesStarted).Milliseconds())
	b.metrics.RecommenderMillis.Add(sourcesStarted.Sub(planStarted).Milliseconds() + time.Since(rankStarted).Milliseconds())
	var rows []Row
	rawRows, _ := ranked["rows"].([]any)
	for _, r := range rawRows {
		if o, ok := r.(map[string]any); ok {
			if row, ok := toRow(o, candidates); ok {
				rows = append(rows, row)
			}
		}
	}
	if rows == nil {
		rows = []Row{}
	}
	items := []youtube.VideoItem{}
	seen := map[string]bool{}
	for _, row := range rows {
		for _, u := range row.VideoURLs {
			if v, ok := candidates.VideosByURL[u]; ok && !seen[u] {
				seen[u] = true
				items = append(items, v)
			}
		}
	}
	return &Page{Algorithm: req.AlgorithmID, Rows: rows, Items: items, Trace: readTrace(req.AlgorithmID, ranked, rows, candidates, time.Now().UnixMilli())}
}

// rank asks by plan id, and by the whole snapshot again when the service forgot that plan.
func (b *Builder) rank(ctx context.Context, plan, snapshot map[string]any, c Candidates, timeout time.Duration) map[string]any {
	byPlan := map[string]any{"candidates": c.Groups, "trace": true}
	if id, ok := plan["plan_id"].(string); ok {
		byPlan["plan_id"] = id
	} else {
		byPlan["snapshot"] = snapshot
	}
	first := b.client.Rank(ctx, byPlan, timeout)
	if first.OK() {
		return first.Body
	}
	if !first.Expired {
		return nil
	}
	return b.client.Rank(ctx, map[string]any{"snapshot": snapshot, "candidates": c.Groups, "trace": true}, timeout).Body
}

func toRow(o map[string]any, c Candidates) (Row, bool) {
	var urls []string
	videos, _ := o["videos"].([]any)
	for _, v := range videos {
		if vo, ok := v.(map[string]any); ok {
			if u, ok := vo["url"].(string); ok {
				if _, known := c.VideosByURL[u]; known {
					urls = append(urls, u)
				}
			}
		}
	}
	if len(urls) == 0 {
		return Row{}, false
	}
	row := Row{ID: str(o, "id"), Title: str(o, "title"), VideoURLs: urls}
	if reason, ok := o["reason"].(map[string]any); ok {
		row.Reason = str(reason, "kind")
		if ref, ok := reason["ref"].(string); ok {
			row.ReasonRef = &ref
		}
	}
	return row, true
}

const maxScores = 60

// readTrace is RecommenderTraceReader.read; nil when the service sent no trace.
func readTrace(algorithm string, ranked map[string]any, rows []Row, c Candidates, builtAt int64) *Trace {
	trace, ok := ranked["trace"].(map[string]any)
	if !ok {
		return nil
	}
	rowOf := map[string]string{}
	for _, row := range rows {
		for _, u := range row.VideoURLs {
			rowOf[u] = row.Title
		}
	}
	why := map[string]string{}
	for _, r := range list(trace["rows"]) {
		why[str(r, "id")] = str(r, "why")
	}
	t := &Trace{Algorithm: algorithm, BuiltAt: builtAt, Sources: []TraceSource{}, Dropped: map[string]int{}, Rows: []TraceRow{}, Scores: []TraceScore{}}
	for _, g := range c.Groups {
		group, _ := g.(map[string]any)
		src, _ := group["source"].(map[string]any)
		detail := ""
		for _, name := range []string{"query", "channel_url", "video_url", "of"} {
			if v := text(src, name); v != "" {
				detail = v
				break
			}
		}
		videos, _ := group["videos"].([]any)
		t.Sources = append(t.Sources, TraceSource{Kind: str(src, "kind"), Detail: detail, Received: len(videos)})
	}
	switch n := trace["candidates_received"].(type) {
	case float64:
		t.CandidatesReceived = int(n)
	}
	for _, d := range list(trace["dropped"]) {
		t.Dropped[str(d, "why")]++
	}
	for _, row := range rows {
		t.Shown += len(row.VideoURLs)
		t.Rows = append(t.Rows, TraceRow{ID: row.ID, Title: row.Title, Why: why[row.ID], Videos: len(row.VideoURLs)})
	}
	var scores []map[string]any
	for _, s := range list(trace["scores"]) {
		if _, ok := rowOf[str(s, "url")]; ok {
			scores = append(scores, s)
		}
	}
	sort.SliceStable(scores, func(i, j int) bool { return num(scores[i]["total"]) > num(scores[j]["total"]) })
	for _, s := range scores[:min(len(scores), maxScores)] {
		u := str(s, "url")
		v := c.VideosByURL[u]
		factors := map[string]float64{}
		if f, ok := s["factors"].(map[string]any); ok {
			for k, x := range f {
				factors[k] = num(x)
			}
		}
		t.Scores = append(t.Scores, TraceScore{URL: u, Title: v.Title, Channel: v.UploaderName, Row: rowOf[u], Total: num(s["total"]), Factors: factors})
	}
	return t
}

func list(v any) []map[string]any {
	l, _ := v.([]any)
	var out []map[string]any
	for _, x := range l {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

func str(m map[string]any, name string) string { return text(m, name) }

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
