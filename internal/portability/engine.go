package portability

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

// DataPort writes imported records to accounts and reads accounts into records.
type DataPort interface {
	Import(ctx context.Context, user string, src Source, req ImportRequest, progress func(Category, int64) error,
		complete func(Category, int64) error) (map[string]int64, error)
	Export(ctx context.Context, user string, categories []Category, sink Sink) error
}

type ImportRequest struct {
	Categories      []Category `json:"categories"`
	DuplicatePolicy string     `json:"duplicatePolicy"`
}

type ExportRequest struct {
	Format     Format     `json:"format"`
	Categories []Category `json:"categories"`
}

// Engine is PortabilityEngine.
type Engine struct {
	Registry *Registry
	Store    *Store
	Port     DataPort
	Log      *slog.Logger
	// Takeout applies YouTube Takeout imports in their staged order; nil applies them like any other format.
	Takeout func(ctx context.Context, e *Engine, j *Job, req ImportRequest, counts map[Category]int64, r *reporter) (map[string]int64, error)
}

func (e *Engine) Formats() []Descriptor { return e.Registry.Descriptors() }

func (e *Engine) start(j *Job, initial State, work func(context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	j.mu.Lock()
	j.cancel, j.done = cancel, done
	j.mu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		if initial != "" {
			if err := j.transition([]State{Queued}, initial, nil, nil); err != nil {
				return
			}
		}
		if err := work(ctx); err != nil {
			if err == errCancelled || ctx.Err() != nil {
				j.tryTransition(active, Cancelled, nil, nil)
				return
			}
			if AsError(err).Status >= 500 {
				e.Log.Error("portability job failed", "jobId", j.ID, "err", err)
			}
			j.fail(err)
		}
	}()
}

// StartImport takes ownership of upload and analyses it in the background.
func (e *Engine) StartImport(owner, upload, filename, contentType string, hint Format, requestID *string) (Snapshot, error) {
	j, err := e.Store.create(owner, "import", requestID)
	if err != nil {
		return Snapshot{}, err
	}
	saved := filepath.Join(j.dir, "upload")
	if err := os.Rename(upload, saved); err != nil {
		e.Store.Remove(owner, j.ID)
		return Snapshot{}, err
	}
	e.start(j, Analyzing, func(ctx context.Context) error {
		defer os.Remove(saved)
		in, err := NewInput(saved, filename, contentType)
		if err != nil {
			return err
		}
		adapter, det, err := e.Registry.Detect(in, hint)
		if err != nil {
			return err
		}
		sp, err := CreateSpool(j.dir)
		if err != nil {
			return err
		}
		j.spool = sp
		r := newReporter(j, "analyzing", "records", nil, 100)
		if err := adapter.Decode(in, progressSink{sp, r}); err != nil {
			return err
		}
		if err := r.finish(); err != nil {
			return err
		}
		p, err := preview(sp, DetectionItem{det.Format, det.FormatVersion, adapter.Descriptor().AdapterVersion, det.Confidence, det.Evidence}, nil)
		if err != nil {
			return err
		}
		return j.transition([]State{Analyzing}, Ready, p, nil)
	})
	return j.Snapshot(), nil
}

func preview(sp *Spool, det DetectionItem, issues []Issue) (*Preview, error) {
	counts, err := sp.Counts()
	if err != nil {
		return nil, err
	}
	dup, err := sp.Duplicates()
	if err != nil {
		return nil, err
	}
	if issues == nil {
		if issues, err = sp.Issues(); err != nil {
			return nil, err
		}
	}
	byName := map[string]int64{}
	for c, n := range counts {
		byName[string(c)] = n
	}
	return &Preview{Detection: det, Counts: byName, Duplicates: dup, Issues: issues}, nil
}

func (e *Engine) StartExport(owner string, req ExportRequest, requestID *string) (Snapshot, error) {
	if len(req.Categories) == 0 {
		return Snapshot{}, invalid("At least one category is required")
	}
	j, err := e.Store.create(owner, "export", requestID)
	if err != nil {
		return Snapshot{}, err
	}
	cats := sortedCategories(req.Categories)
	e.start(j, Encoding, func(ctx context.Context) error {
		adapter, err := e.Registry.Adapter(req.Format)
		if err != nil {
			return err
		}
		sp, err := CreateSpool(j.dir)
		if err != nil {
			return err
		}
		j.spool = sp
		collect := newReporter(j, "collecting", "records", nil, 100)
		if err := e.Port.Export(ctx, owner, cats, progressSink{sp, collect}); err != nil {
			return err
		}
		if err := collect.finish(); err != nil {
			return err
		}
		issues := assessExport(adapter, sp, cats)
		for _, i := range issues {
			if i.Code == "unsupported_export_category" {
				return invalid("Export category is unsupported")
			}
		}
		artifact := filepath.Join(j.dir, "export."+adapter.Descriptor().DefaultExtension)
		f, err := os.Create(artifact)
		if err != nil {
			return err
		}
		enc := newReporter(j, "encoding", "bytes", nil, 64<<10)
		w := bufio.NewWriterSize(&countingWriter{f, enc}, 64<<10)
		err = adapter.Encode(sp, w, cats)
		if err == nil {
			err = w.Flush()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if err := enc.finish(); err != nil {
			return err
		}
		j.artifact = artifact
		if issues == nil {
			issues = []Issue{}
		}
		p, err := preview(sp, DetectionItem{req.Format, nil, adapter.Descriptor().AdapterVersion, 100, "Requested export format"}, issues)
		if err != nil {
			return err
		}
		return j.transition([]State{Encoding}, Completed, p, nil)
	})
	return j.Snapshot(), nil
}

// exportAssessor is an adapter that reports more than the categories it cannot write.
type exportAssessor interface {
	AssessExport(Source, []Category) []Issue
}

func assessExport(a Adapter, src Source, cats []Category) []Issue {
	if x, ok := a.(exportAssessor); ok {
		return x.AssessExport(src, cats)
	}
	var issues []Issue
	for _, c := range cats {
		if !exportable(a.Descriptor(), c) {
			issues = append(issues, Issue{c, "unsupported_export_category", string(c) + " cannot be exported", 1})
		}
	}
	return issues
}

type countingWriter struct {
	w io.Writer
	r *reporter
}

func (c *countingWriter) Write(b []byte) (int, error) {
	if err := c.r.active(); err != nil {
		return 0, err
	}
	n, err := c.w.Write(b)
	if err == nil {
		err = c.r.add(int64(n))
	}
	return n, err
}

func sortedCategories(cs []Category) []Category {
	seen := map[Category]bool{}
	var out []Category
	for _, c := range cs {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	// Kotlin sorts by name, which puts subscriptionGroups before subscriptions and drops every membership of a
	// subscription imported in the same run; subscriptions go first here.
	rank := func(c Category) string {
		if c == SubscriptionGroups {
			return string(Subscriptions) + "~"
		}
		return string(c)
	}
	sort.Slice(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

func (e *Engine) Snapshot(owner, id string) (Snapshot, error) {
	j, err := e.Store.Get(owner, id)
	if err != nil {
		return Snapshot{}, err
	}
	return j.Snapshot(), nil
}

func (e *Engine) Report(owner, id string) (Report, error) {
	j, err := e.Store.Get(owner, id)
	if err != nil {
		return Report{}, err
	}
	return j.Report(), nil
}

func (e *Engine) Artifact(owner, id string) (string, error) {
	j, err := e.Store.Get(owner, id)
	if err != nil {
		return "", err
	}
	if j.Snapshot().State != Completed {
		return "", badState()
	}
	if j.artifact == "" {
		return "", invalid("Export artifact is unavailable")
	}
	return j.artifact, nil
}

func (e *Engine) Apply(owner, id string, req ImportRequest) (Snapshot, error) {
	if len(req.Categories) == 0 {
		return Snapshot{}, invalid("At least one category is required")
	}
	j, err := e.Store.Get(owner, id)
	if err != nil {
		return Snapshot{}, err
	}
	snap := j.Snapshot()
	if snap.Preview == nil {
		return Snapshot{}, invalid("Import preview is not ready")
	}
	for _, c := range req.Categories {
		if _, ok := snap.Preview.Counts[string(c)]; !ok {
			return Snapshot{}, invalid("A selected category is unavailable")
		}
	}
	if err := j.transition([]State{Ready}, Applying, nil, nil); err != nil {
		return Snapshot{}, err
	}
	req.Categories = sortedCategories(req.Categories)
	e.start(j, "", func(ctx context.Context) error {
		counts, err := j.spool.Counts()
		if err != nil {
			return err
		}
		total := int64(0)
		for _, c := range req.Categories {
			total += counts[c]
		}
		r := newReporter(j, "applying", "records", &total, interval(total))
		var result map[string]int64
		if snap.Preview.Detection.Format == YoutubeTakout && e.Takeout != nil {
			result, err = e.Takeout(ctx, e, j, req, counts, r)
		} else {
			completed := map[string]int64{}
			result, err = e.Port.Import(ctx, owner, j.spool, req,
				func(c Category, n int64) error {
					total := counts[c]
					r.setStage("remaining", c, &total)
					return r.add(n)
				},
				func(c Category, n int64) error {
					total := counts[c]
					r.setStage("remaining", c, &total)
					completed[string(c)] = n
					copied := map[string]int64{}
					for k, v := range completed {
						copied[k] = v
					}
					j.update(func(s *Snapshot) { s.Result = copied })
					return r.checkpoint()
				})
		}
		if err != nil {
			return err
		}
		if err := r.finish(); err != nil {
			return err
		}
		return j.transition([]State{Applying}, Completed, nil, result)
	})
	return j.Snapshot(), nil
}

func (e *Engine) Cancel(owner, id string) (Snapshot, error) {
	j, err := e.Store.Get(owner, id)
	if err != nil {
		return Snapshot{}, err
	}
	if terminal(j.Snapshot().State) {
		return j.Snapshot(), nil
	}
	if j.tryTransition(active, Cancelled, nil, nil) && j.cancel != nil {
		j.cancel()
	}
	return j.Snapshot(), nil
}

func (e *Engine) Delete(owner, id string) error {
	j, err := e.Store.Get(owner, id)
	if err != nil {
		return err
	}
	if j.cancelled() && !j.taskDone() {
		return badState()
	}
	return e.Store.Remove(owner, id)
}
