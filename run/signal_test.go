package run

import (
	"bytes"
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/pmuston/notekit/exec"
)

// TestSignalTeardown is the M1 gate's signal-safe teardown, verified under both signals
// rather than by inspection.
//
// It is safe to send these to the test process because HandleSignals has installed a
// handler: an unhandled SIGINT or SIGTERM would kill the test binary, which is exactly
// why the assertion is worth making — a tool that forgets this leaks a pty child or a
// database connection on every Ctrl-C.
func TestSignalTeardown(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			h := newHarness(t, "## A\n\n```echo\nx\n```\n")
			sess := h.session(t)

			var errOut bytes.Buffer
			done, stop := h.s.HandleSignals(&errOut)
			defer stop()

			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatalf("sending %v: %v", sig, err)
			}

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("shutdown did not complete after %v", sig)
			}

			if !sess.Closed() {
				t.Errorf("%v did not destroy the session", sig)
			}
			if errOut.Len() != 0 {
				t.Errorf("shutdown reported: %s", errOut.String())
			}
			// The scheduler is shut down, so further work is refused rather than
			// silently accepted into a dead queue.
			if _, err := h.s.Submit(h.path, 0); err == nil {
				t.Error("Submit after signal shutdown = nil error, want error")
			}
		})
	}
}

// TestSignalTeardownDrainsQueuedRuns: a run already accepted is completed, not dropped.
func TestSignalTeardownDrainsQueuedRuns(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo {delay=30ms}\nfirst\n```\n\n## B\n\n```echo {delay=30ms}\nsecond\n```\n")

	for i := 0; i < 2; i++ {
		if _, err := h.s.Submit(h.path, i); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	done, stop := h.s.HandleSignals(nil)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete")
	}

	for _, r := range h.s.Runs() {
		if r.State != Done {
			t.Errorf("run %s: state = %v, want done (%v)", r.ID, r.State, r.Err)
		}
	}
	for i, c := range mustCells(t, h.contents()) {
		if len(c.Results) != 1 {
			t.Errorf("cell %d has %d results, want 1", i, len(c.Results))
		}
	}
}

// TestStopWithoutSignal covers the ordinary exit path: a tool that shuts down cleanly
// calls stop from a defer and no signal ever arrives.
func TestStopWithoutSignal(t *testing.T) {
	h := newHarness(t, "## A\n\n```echo\nx\n```\n")
	// Capture the session before shutting down: Shutdown removes the notebook from
	// the scheduler, so it is no longer reachable afterwards.
	sess := h.session(t)
	done, stop := h.s.HandleSignals(nil)

	stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler goroutine did not exit after stop")
	}

	// stop is idempotent, so a tool may call it from both a defer and an explicit
	// shutdown path without a double-close panic.
	stop()

	// The scheduler was never shut down by a signal, so it still works.
	if r := h.runCell(0); r.State != Done {
		t.Errorf("state = %v after stop() (%v)", r.State, r.Err)
	}
	if err := h.s.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if !sess.Closed() {
		t.Error("session not closed")
	}
}

// TestShutdownReportsSessionCloseErrors: a broken engine must not hide, and must not
// stop the other sessions being closed.
func TestShutdownReportsSessionCloseErrors(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/notes.md"
	if err := os.WriteFile(path, []byte(front+"## A\n\n```stubborn\nx\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := dir + "/other.md"
	if err := os.WriteFile(other, []byte(front+"## A\n\n```stubborn\nx\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := New()
	ex := &stubbornExecutor{}
	if err := s.Open(context.Background(), path, ex); err != nil {
		t.Fatal(err)
	}
	if err := s.Open(context.Background(), other, ex); err != nil {
		t.Fatal(err)
	}

	err := s.Shutdown(context.Background())
	if err == nil {
		t.Fatal("Shutdown = nil error, want the close failures surfaced")
	}
	// Both sessions were attempted even though the first failed.
	if got := ex.closes(); got != 2 {
		t.Errorf("Close called %d times, want 2 — one failure must not skip the rest", got)
	}
}

// stubbornExecutor's sessions always fail to close. Shutdown closes sessions
// sequentially, so a plain counter suffices.
type stubbornExecutor struct{ n int }

func (*stubbornExecutor) Lang() string { return "stubborn" }
func (e *stubbornExecutor) Open(context.Context, exec.Notebook) (exec.Session, error) {
	return &stubbornSession{e: e}, nil
}
func (e *stubbornExecutor) closes() int { return e.n }

type stubbornSession struct{ e *stubbornExecutor }

func (s *stubbornSession) Execute(context.Context, exec.Request) (exec.Result, error) {
	return exec.Result{}, nil
}
func (s *stubbornSession) Close(context.Context) error {
	s.e.n++
	return errors.New("stubborn: refusing to close")
}
