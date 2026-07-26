// Package run is the run scheduler.
//
// Implements notekit-kit-spec.md §3.4. A run request returns a run ID immediately and
// its state is pollable (harvest R3); there is no streaming. Runs within one notebook
// are serialised in request order because sessions are stateful; different notebooks run
// concurrently.
//
// On completion this package builds the `output` or `error` block with canonical
// metadata, applies fence-length safety, splices via package doc, and saves (harvest
// R5). Capture is capped at [doc.OutputCap] with ANSI stripped and a truncation marker
// appended (format spec §6). Results are volatile: a run replaces the whole of the
// cell's result position.
//
// This package also owns SIGINT and SIGTERM handling and calls every live session's
// destroy hook, so teardown is reliable.
//
// # Done versus Failed
//
// The distinction matters and is easy to get backwards. A cell that ran and whose
// *domain* reported a failure — a non-zero exit status, a database error — is a
// **successful run** that persists an `error` block: state [Done], with
// [Run.Form] reporting `error`. [Failed] means the run itself could not complete: no
// session, a refused splice, an unwritable file. Nothing is persisted for a Failed run,
// because a run that could not finish has no result to record.
package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/exec"
	"github.com/pmuston/notekit/kind"
)

// State is a run's lifecycle state.
type State uint8

const (
	// Queued: accepted, waiting for the notebook's worker. Runs within a notebook
	// are serialised, so a queued run is waiting on an earlier one.
	Queued State = iota
	// Running: handed to the executor.
	Running
	// Done: the cell ran and its result was persisted. A domain failure is Done with
	// Form == doc.ResultError, not Failed.
	Done
	// Failed: the run could not complete. Nothing was persisted; Err says why.
	Failed
	// Cancelled: the caller cancelled the run. Nothing was persisted.
	Cancelled
)

func (s State) String() string {
	switch s {
	case Queued:
		return "queued"
	case Running:
		return "running"
	case Done:
		return "done"
	case Failed:
		return "failed"
	case Cancelled:
		return "cancelled"
	}
	return "unknown"
}

// Terminal reports whether the state will not change again.
func (s State) Terminal() bool { return s == Done || s == Failed || s == Cancelled }

// DefaultQueueSize is how many runs may be pending for one notebook.
const DefaultQueueSize = 64

// ID identifies a run within a scheduler.
type ID string

// Run is a snapshot of one run's state. It is a copy: polling never races with the
// worker that owns the run.
type Run struct {
	ID        ID
	Notebook  string // notebook path
	CellIndex int
	Cell      string // heading text, for display

	State State
	Err   error // set when State is Failed

	// Form is what was persisted, valid when State is Done.
	Form doc.ResultForm

	// Truncated reports whether the durable output hit the cap.
	Truncated bool

	Submitted time.Time
	Started   time.Time
	Finished  time.Time
}

// Scheduler owns open notebooks, their executor sessions, and their run queues.
type Scheduler struct {
	tool      string
	registry  *kind.Registry
	cap       int
	queueSize int
	now       func() time.Time
	newID     func() (string, error)

	mu        sync.Mutex
	notebooks map[string]*openNotebook
	runs      map[ID]*Run
	cancels   map[ID]context.CancelFunc
	nextID    int
	closed    bool
}

// Option configures a Scheduler.
type Option func(*Scheduler)

// WithTool sets the `tool` provenance value written into result metadata, as
// name/version (format spec §6).
func WithTool(nameVersion string) Option {
	return func(s *Scheduler) { s.tool = nameVersion }
}

// WithRegistry supplies the kind registry. Defaults to [kind.NewRegistry], the core
// kinds every conforming tool must support.
func WithRegistry(r *kind.Registry) Option {
	return func(s *Scheduler) { s.registry = r }
}

// WithOutputCap overrides the durable output cap. Defaults to [doc.OutputCap]; tests
// use a small value to exercise truncation without a megabyte of data.
func WithOutputCap(n int) Option {
	return func(s *Scheduler) { s.cap = n }
}

// WithClock injects the clock. Result metadata carries an RFC 3339 timestamp, so a
// deterministic clock is what makes golden output stable.
func WithClock(now func() time.Time) Option {
	return func(s *Scheduler) { s.now = now }
}

// WithQueueSize sets how many runs may be pending for one notebook before Submit
// applies backpressure. Defaults to [DefaultQueueSize].
//
// The queue is bounded on purpose: an unbounded one turns a runaway submitter into
// unbounded memory, and refusing a submission is a better failure than accepting work
// that will never be reached.
func WithQueueSize(n int) Option {
	return func(s *Scheduler) {
		if n > 0 {
			s.queueSize = n
		}
	}
}

// WithIDGenerator injects the cell-id generator, for the same reason as WithClock:
// a run may assign an id (format spec §5.1), and goldens need it to be predictable.
func WithIDGenerator(gen func() (string, error)) Option {
	return func(s *Scheduler) { s.newID = gen }
}

// New returns a Scheduler.
func New(opts ...Option) *Scheduler {
	s := &Scheduler{
		tool:      "notekit",
		registry:  kind.NewRegistry(),
		cap:       doc.OutputCap,
		queueSize: DefaultQueueSize,
		now:       time.Now,
		newID:     doc.NewID,
		notebooks: make(map[string]*openNotebook),
		runs:      make(map[ID]*Run),
		cancels:   make(map[ID]context.CancelFunc),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// openNotebook is one notebook's session, parse state, and serialised queue.
type openNotebook struct {
	path string
	ex   exec.Executor
	sess exec.Session

	// queue serialises runs: sessions are stateful, so concurrent cell execution
	// within one notebook is forbidden (kit spec §3.4).
	queue chan *pending
	done  chan struct{}

	mu  sync.Mutex
	nb  *doc.Notebook // reparsed after every write
	src []byte
}

type pending struct {
	id     ID
	index  int
	ctx    context.Context
	cancel context.CancelFunc
}

// Open reads a notebook, creates its executor session, and starts its worker.
//
// The notebook is keyed by its cleaned path; opening the same path twice is an error
// rather than a second session, because one session per notebook is the invariant every
// stateful executor depends on (harvest R1).
func (s *Scheduler) Open(ctx context.Context, path string, ex exec.Executor) error {
	path = filepath.Clean(path)

	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("run: opening %s: %w", path, err)
	}
	nb, err := doc.Parse(src)
	if err != nil {
		return fmt.Errorf("run: %s: %w", path, err)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("run: scheduler is shut down")
	}
	if _, exists := s.notebooks[path]; exists {
		s.mu.Unlock()
		return fmt.Errorf("run: %s is already open", path)
	}
	s.mu.Unlock()

	sess, err := ex.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("run: opening session for %s: %w", path, err)
	}

	on := &openNotebook{
		path:  path,
		ex:    ex,
		sess:  sess,
		queue: make(chan *pending, s.queueSize),
		done:  make(chan struct{}),
		nb:    nb,
		src:   src,
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = sess.Close(ctx)
		return errors.New("run: scheduler is shut down")
	}
	if _, exists := s.notebooks[path]; exists {
		s.mu.Unlock()
		_ = sess.Close(ctx)
		return fmt.Errorf("run: %s is already open", path)
	}
	s.notebooks[path] = on
	s.mu.Unlock()

	go s.worker(on)
	return nil
}

// Cells returns the notebook's cells as last parsed.
func (s *Scheduler) Cells(path string) ([]*doc.Cell, error) {
	on, err := s.notebook(path)
	if err != nil {
		return nil, err
	}
	on.mu.Lock()
	defer on.mu.Unlock()
	return on.nb.Cells(), nil
}

func (s *Scheduler) notebook(path string) (*openNotebook, error) {
	path = filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	on, ok := s.notebooks[path]
	if !ok {
		return nil, fmt.Errorf("run: %s is not open", path)
	}
	return on, nil
}

// Submit queues a run for one cell and returns its ID immediately (harvest R3).
//
// The cell is addressed by index in document order. Indices are stable across result
// writes — persisting a result never adds or removes a cell — so a caller may hold an
// index across runs, but must re-list after editing the notebook.
func (s *Scheduler) Submit(path string, cellIndex int) (ID, error) {
	on, err := s.notebook(path)
	if err != nil {
		return "", err
	}

	on.mu.Lock()
	cells := on.nb.Cells()
	if cellIndex < 0 || cellIndex >= len(cells) {
		on.mu.Unlock()
		return "", fmt.Errorf("run: %s has %d cells, so index %d is out of range", path, len(cells), cellIndex)
	}
	heading := cells[cellIndex].HeadingText
	lang := cells[cellIndex].Lang
	on.mu.Unlock()

	if lang != on.ex.Lang() {
		return "", fmt.Errorf("run: cell %d is %q but the executor claims %q", cellIndex, lang, on.ex.Lang())
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", errors.New("run: scheduler is shut down")
	}
	s.nextID++
	id := ID(fmt.Sprintf("r%d", s.nextID))
	s.runs[id] = &Run{
		ID:        id,
		Notebook:  on.path,
		CellIndex: cellIndex,
		Cell:      heading,
		State:     Queued,
		Submitted: s.now(),
	}
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[id] = cancel
	s.mu.Unlock()

	select {
	case on.queue <- &pending{id: id, index: cellIndex, ctx: ctx, cancel: cancel}:
		return id, nil
	default:
		cancel()
		s.update(id, func(r *Run) {
			r.State = Failed
			r.Err = errors.New("run: queue is full")
			r.Finished = s.now()
		})
		return id, fmt.Errorf("run: queue for %s is full", path)
	}
}

// State returns a snapshot of a run.
func (s *Scheduler) State(id ID) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, false
	}
	return *r, true
}

// Runs returns snapshots of every run, oldest first.
func (s *Scheduler) Runs() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Submitted.Equal(out[j].Submitted) {
			return out[i].ID < out[j].ID
		}
		return out[i].Submitted.Before(out[j].Submitted)
	})
	return out
}

// Cancel cancels a queued or running run. Cancelling a finished run does nothing, so a
// caller racing the worker does not have to guard against it.
func (s *Scheduler) Cancel(id ID) error {
	s.mu.Lock()
	r, ok := s.runs[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("run: no run %s", id)
	}
	if r.State.Terminal() {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancels[id]
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// update mutates a run under the scheduler lock.
func (s *Scheduler) update(id ID, f func(*Run)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[id]; ok {
		f(r)
	}
}
