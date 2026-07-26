// Package echoexec is a trivial executor that exercises the exec contract without a
// real engine.
//
// It is the M1 deliverable's executor and a reference implementation: a new domain
// executor should look like this, only with an engine behind it. It is also what the
// run scheduler's tests drive, so the contract is exercised by something other than the
// package that defines it.
//
// Cell metadata drives its behaviour, which is how it reaches every branch of the
// runtime without a shell:
//
//	```echo                     the source is echoed back as `text`
//	```echo {format=csv}        echoed back as a `table` with csv serialisation
//	```echo {fail}              a domain error with no status
//	```echo {fail, status=127}  a domain error carrying that status
//	```echo {broken}            a *session* failure: the run cannot complete at all
//	```echo {repeat=2000}       the source repeated, for exercising the output cap
//	```echo {delay=50ms}        sleeps first, so cancellation has something to cancel
//	```echo {kind=nosuch}       returns an unregistered kind
package echoexec

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pmuston/notekit/exec"
	"github.com/pmuston/notekit/kind"
)

// Lang is the info-string tag this executor claims.
const Lang = "echo"

// Executor implements [exec.Executor].
type Executor struct{}

// New returns an echo executor.
func New() *Executor { return &Executor{} }

func (*Executor) Lang() string { return Lang }

// Open creates a session. The notebook path is recorded but never written to.
func (*Executor) Open(_ context.Context, nb exec.Notebook) (exec.Session, error) {
	return &Session{path: nb.Path}, nil
}

// Session implements [exec.Session]. It records what it was asked to do so tests can
// assert the runtime's behaviour, including that teardown really happened.
type Session struct {
	path string

	mu     sync.Mutex
	closed bool
	runs   int
	// Concurrent is set if Execute is ever entered twice at once, which the
	// scheduler must never allow: sessions are stateful (kit spec §3.4).
	Concurrent bool
	active     int
}

// Runs returns how many cells this session executed.
func (s *Session) Runs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs
}

// Closed reports whether Close has been called — the assertion that matters for
// signal-safe teardown.
func (s *Session) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// SawConcurrentExecute reports whether two executions overlapped.
func (s *Session) SawConcurrentExecute() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Concurrent
}

func (s *Session) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *Session) Execute(ctx context.Context, req exec.Request) (exec.Result, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return exec.Result{}, errors.New("echoexec: session is closed")
	}
	s.runs++
	s.active++
	if s.active > 1 {
		s.Concurrent = true
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()

	opt := options(req)

	if opt.delay > 0 {
		select {
		case <-ctx.Done():
			return exec.Result{}, ctx.Err()
		case <-time.After(opt.delay):
		}
	}
	if err := ctx.Err(); err != nil {
		return exec.Result{}, err
	}

	// A session failure, distinct from a domain failure: the run cannot complete, so
	// nothing is persisted.
	if opt.broken {
		return exec.Result{}, errors.New("echoexec: the engine broke")
	}

	// A domain failure persists as a first-class error block (format spec §7).
	if opt.fail {
		msg := strings.TrimRight(req.Source, "\n")
		if msg == "" {
			msg = "echoexec: deliberate failure"
		}
		if opt.status != nil {
			return exec.Result{}, exec.NewError(*opt.status, "%s", msg)
		}
		return exec.Result{}, exec.NewErrorNoStatus("%s", msg)
	}

	body := req.Source
	if opt.repeat > 1 {
		body = strings.Repeat(req.Source, opt.repeat)
	}

	switch {
	case opt.kindName != "":
		return exec.Result{Kind: opt.kindName, Payload: body}, nil
	case opt.format == kind.CSV || opt.format == kind.JSONL:
		return exec.Result{
			Kind:    kind.Table,
			Payload: kind.TablePayload{Format: opt.format, Body: body},
		}, nil
	default:
		return exec.Result{Kind: kind.Text, Payload: body}, nil
	}
}

type opts struct {
	format   string
	kindName string
	fail     bool
	broken   bool
	status   *int
	repeat   int
	delay    time.Duration
}

// options reads the cell's metadata. The format assigns no meaning to source-fence
// keys (§9), so interpreting them is exactly the executor's job.
func options(req exec.Request) opts {
	o := opts{repeat: 1}
	if req.Meta == nil {
		return o
	}
	if e, ok := req.Meta.Get("format"); ok {
		o.format = e.Value
	}
	if e, ok := req.Meta.Get("kind"); ok {
		o.kindName = e.Value
	}
	if _, ok := req.Meta.Get("fail"); ok {
		o.fail = true
	}
	if _, ok := req.Meta.Get("broken"); ok {
		o.broken = true
	}
	if e, ok := req.Meta.Get("status"); ok {
		if n, err := strconv.Atoi(e.Value); err == nil {
			o.status = &n
			o.fail = true
		}
	}
	if e, ok := req.Meta.Get("repeat"); ok {
		if n, err := strconv.Atoi(e.Value); err == nil && n > 0 {
			o.repeat = n
		}
	}
	if e, ok := req.Meta.Get("delay"); ok {
		if d, err := time.ParseDuration(e.Value); err == nil {
			o.delay = d
		}
	}
	return o
}

// String makes a session inspectable in test failures.
func (s *Session) String() string {
	return fmt.Sprintf("echoexec.Session{path:%s runs:%d closed:%t}", s.path, s.Runs(), s.Closed())
}
