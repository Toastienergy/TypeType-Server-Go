package portability

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"typetype.dev/server/internal/auth"
)

type State string

const (
	Queued    State = "queued"
	Analyzing State = "analyzing"
	Ready     State = "ready"
	Applying  State = "applying"
	Encoding  State = "encoding"
	Completed State = "completed"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

func terminal(s State) bool { return s == Completed || s == Failed || s == Cancelled }

type JobProgress struct {
	Phase          string    `json:"phase"`
	Unit           string    `json:"unit"`
	Processed      int64     `json:"processed"`
	Total          *int64    `json:"total"`
	Category       *Category `json:"category"`
	Stage          *string   `json:"stage"`
	StageProcessed int64     `json:"stageProcessed"`
	StageTotal     *int64    `json:"stageTotal"`
	Checkpoint     int64     `json:"checkpoint"`
}

type DetectionItem struct {
	Format         Format  `json:"format"`
	FormatVersion  *string `json:"formatVersion"`
	AdapterVersion int     `json:"adapterVersion"`
	Confidence     int     `json:"confidence"`
	Evidence       string  `json:"evidence"`
}

type Preview struct {
	Detection  DetectionItem    `json:"detection"`
	Counts     map[string]int64 `json:"counts"`
	Duplicates int64            `json:"duplicates"`
	Issues     []Issue          `json:"issues"`
}

// Snapshot is PortabilityJobSnapshot.
type Snapshot struct {
	ID           string           `json:"id"`
	Kind         string           `json:"kind"`
	State        State            `json:"state"`
	CreatedAt    int64            `json:"createdAt"`
	UpdatedAt    int64            `json:"updatedAt"`
	RequestID    *string          `json:"requestId"`
	Preview      *Preview         `json:"preview"`
	Result       map[string]int64 `json:"result"`
	Progress     *JobProgress     `json:"progress"`
	ErrorCode    *string          `json:"errorCode"`
	ErrorMessage *string          `json:"errorMessage"`
}

// Report is PortabilityJobReport.
type Report struct {
	ID           string           `json:"id"`
	State        State            `json:"state"`
	RequestID    *string          `json:"requestId"`
	Preview      *Preview         `json:"preview"`
	Result       map[string]int64 `json:"result"`
	ErrorCode    *string          `json:"errorCode"`
	ErrorMessage *string          `json:"errorMessage"`
}

type Job struct {
	ID, Owner string
	dir       string
	mu        sync.Mutex
	snap      Snapshot
	cancel    context.CancelFunc
	done      chan struct{}
	spool     *Spool
	artifact  string
}

func now() int64 { return time.Now().UnixMilli() }

func (j *Job) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snap
}

func (j *Job) Report() Report {
	s := j.Snapshot()
	return Report{s.ID, s.State, s.RequestID, s.Preview, s.Result, s.ErrorCode, s.ErrorMessage}
}

func (j *Job) cancelled() bool { return j.Snapshot().State == Cancelled }

func (j *Job) taskDone() bool {
	j.mu.Lock()
	done := j.done
	j.mu.Unlock()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (j *Job) update(fn func(*Snapshot)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if terminal(j.snap.State) {
		return
	}
	fn(&j.snap)
	j.snap.UpdatedAt = now()
}

// transition moves from one of expected to state, clearing any error, or fails with an invalid-state error.
func (j *Job) transition(expected []State, state State, preview *Preview, result map[string]int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !in(j.snap.State, expected) {
		return badState()
	}
	j.snap.State, j.snap.UpdatedAt, j.snap.ErrorCode, j.snap.ErrorMessage = state, now(), nil, nil
	if preview != nil {
		j.snap.Preview = preview
	}
	if result != nil {
		j.snap.Result = result
	}
	return nil
}

func (j *Job) tryTransition(expected []State, state State, code, msg *string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !in(j.snap.State, expected) {
		return false
	}
	j.snap.State, j.snap.UpdatedAt, j.snap.ErrorCode, j.snap.ErrorMessage = state, now(), code, msg
	return true
}

var active = []State{Queued, Analyzing, Ready, Applying, Encoding}

func in(s State, set []State) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

func (j *Job) fail(err error) {
	e := AsError(err)
	j.tryTransition(active, Failed, &e.Code, &e.Message)
}

func (j *Job) remove() {
	if j.cancel != nil {
		j.cancel()
	}
	if j.spool != nil {
		j.spool.Delete()
	}
	os.RemoveAll(j.dir)
}

// Store is PortabilityJobStore: jobs live in memory with their files under root, and finished ones go after a day.
type Store struct {
	root string
	mu   sync.Mutex
	jobs map[string]*Job
}

const retention = 24 * time.Hour

func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Store{root: root, jobs: map[string]*Job{}}, nil
}

func (s *Store) create(owner, kind string, requestID *string) (*Job, error) {
	s.cleanup()
	id := auth.UUID()
	dir := filepath.Join(s.root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	t := now()
	j := &Job{ID: id, Owner: owner, dir: dir,
		snap: Snapshot{ID: id, Kind: kind, State: Queued, CreatedAt: t, UpdatedAt: t, RequestID: requestID}}
	s.mu.Lock()
	s.jobs[id] = j
	s.mu.Unlock()
	return j, nil
}

func (s *Store) Get(owner, id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil || j.Owner != owner {
		return nil, ErrNotFound
	}
	return j, nil
}

func (s *Store) Remove(owner, id string) error {
	j, err := s.Get(owner, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.jobs, id)
	s.mu.Unlock()
	j.remove()
	return nil
}

func (s *Store) cleanup() {
	oldest := time.Now().Add(-retention).UnixMilli()
	s.mu.Lock()
	var gone []*Job
	for id, j := range s.jobs {
		snap := j.Snapshot()
		if terminal(snap.State) && j.taskDone() && snap.UpdatedAt < oldest {
			delete(s.jobs, id)
			gone = append(gone, j)
		}
	}
	s.mu.Unlock()
	for _, j := range gone {
		j.remove()
	}
}

// reporter is PortabilityProgressReporter: it publishes every interval records and at every forced point, and
// stops the work as soon as the job is cancelled.
type reporter struct {
	job                         *Job
	phase, unit                 string
	total                       *int64
	interval                    int64
	processed, published        int64
	category                    *Category
	stage                       *string
	stageProcessed, checkpoints int64
	stageTotal                  *int64
}

var errCancelled = &Error{Status: 409, Code: "portability_cancelled", Message: "Portability job was cancelled"}

func newReporter(j *Job, phase, unit string, total *int64, interval int64) *reporter {
	r := &reporter{job: j, phase: phase, unit: unit, total: total, interval: interval, published: -1}
	r.publish(true)
	return r
}

func interval(total int64) int64 { return min(max(total/100, 1), 100) }

func (r *reporter) active() error {
	if r.job.cancelled() {
		return errCancelled
	}
	return nil
}

func (r *reporter) setStage(stage string, c Category, total *int64) {
	if r.stage != nil && *r.stage == stage && r.category != nil && *r.category == c && eq(r.stageTotal, total) {
		return
	}
	r.stage, r.category, r.stageProcessed, r.stageTotal = &stage, &c, 0, total
	r.publish(true)
}

func eq(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (r *reporter) add(n int64) error {
	if err := r.active(); err != nil {
		return err
	}
	r.processed += n
	r.stageProcessed += n
	r.publish(false)
	return nil
}

func (r *reporter) checkpoint() error {
	if err := r.active(); err != nil {
		return err
	}
	r.checkpoints++
	r.publish(true)
	return nil
}

func (r *reporter) finish() error {
	if err := r.active(); err != nil {
		return err
	}
	r.publish(true)
	return nil
}

func (r *reporter) publish(force bool) {
	if !force && r.processed-r.published < r.interval {
		return
	}
	p := JobProgress{Phase: r.phase, Unit: r.unit, Processed: r.processed, Total: r.total, Category: r.category,
		Stage: r.stage, StageProcessed: r.stageProcessed, StageTotal: r.stageTotal, Checkpoint: r.checkpoints}
	r.job.update(func(s *Snapshot) { s.Progress = &p })
	r.published = r.processed
}

// progressSink counts every record written and stops a cancelled job.
type progressSink struct {
	Sink
	r *reporter
}

func (p progressSink) Write(rec *Record) (bool, error) {
	if err := p.r.active(); err != nil {
		return false, err
	}
	ok, err := p.Sink.Write(rec)
	if err == nil {
		err = p.r.add(1)
	}
	return ok, err
}
