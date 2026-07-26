package run

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// HandleSignals arranges for SIGINT and SIGTERM to shut the scheduler down, calling
// every live session's destroy hook (kit spec §3.3).
//
// The kit owns this rather than each tool, because reliable teardown is exactly the
// thing that rots when it is left to a `main` to remember: an executor holding a pty
// child or a database connection leaks it on every Ctrl-C otherwise.
//
// It returns a stop function that removes the handler, restoring the default
// behaviour. Call it from a defer in main; a tool that never calls it is still correct,
// but a test must, or a later signal in the same process would hit a dead handler.
//
// The returned channel closes once a signal has been received and shutdown has
// finished, so a main can wait for it and then exit. Any shutdown error is written to
// errOut if non-nil — a signal handler has nowhere to return an error to, and silently
// swallowing a failed session teardown is how a leak becomes invisible.
func (s *Scheduler) HandleSignals(errOut io.Writer) (done <-chan struct{}, stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if _, ok := <-ch; !ok {
			return // stop() was called before any signal arrived
		}
		if err := s.Shutdown(context.Background()); err != nil && errOut != nil {
			fmt.Fprintf(errOut, "notekit: shutdown: %v\n", err)
		}
	}()

	var stopped bool
	return finished, func() {
		if stopped {
			return
		}
		stopped = true
		signal.Stop(ch)
		close(ch)
	}
}
