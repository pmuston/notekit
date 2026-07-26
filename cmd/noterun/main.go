// Command noterun runs notekit notebook cells from the command line.
//
// It is the M1 demo: the full async loop — submit, poll, persist — driven by the echo
// executor, with no server involved. It is also the shape a real tool's `main` takes,
// so clinote v2 at M3 should read much like this with a shell executor swapped in.
//
//	noterun notebook.md            run every echo cell in document order
//	noterun -cell 2 notebook.md    run one cell by index
//	noterun -list notebook.md      list cells without running anything
//
// Exit status is 0 when every run finished, 1 when any run failed, and 2 for a usage or
// I/O failure — the same split as notefmt.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pmuston/notekit/exec/echoexec"
	"github.com/pmuston/notekit/run"
)

const (
	exitOK      = 0
	exitProblem = 1
	exitUsage   = 2
)

// pollInterval matches the UI's polling cadence (harvest R3), so the demo exercises
// the same pattern package serve will use at M2.
const pollInterval = 50 * time.Millisecond

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("noterun", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cell := fs.Int("cell", -1, "run only this cell index; default is every cell")
	list := fs.Bool("list", false, "list cells and exit")
	timeout := fs.Duration("timeout", 30*time.Second, "give up waiting for a run after this long")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: noterun [flags] <notebook.md>\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}
	path := fs.Arg(0)

	ctx := context.Background()
	s := run.New(
		run.WithTool("noterun/0.1"),
		// A real tool registers its own kinds here; the echo executor only needs the
		// core set.
	)

	// The kit owns signal handling so a Ctrl-C destroys every live session rather
	// than leaking whatever engine sat behind it.
	signalled, stop := s.HandleSignals(stderr)
	defer stop()

	if err := s.Open(ctx, path, echoexec.New()); err != nil {
		fmt.Fprintf(stderr, "noterun: %v\n", err)
		return exitUsage
	}
	defer func() { _ = s.Shutdown(ctx) }()

	cells, err := s.Cells(path)
	if err != nil {
		fmt.Fprintf(stderr, "noterun: %v\n", err)
		return exitUsage
	}

	if *list {
		for i, c := range cells {
			fmt.Fprintf(stdout, "%d\t%s\t%s\n", i, c.Lang, c.HeadingText)
		}
		return exitOK
	}

	targets := make([]int, 0, len(cells))
	switch {
	case *cell >= 0:
		if *cell >= len(cells) {
			fmt.Fprintf(stderr, "noterun: %s has %d cells, so index %d is out of range\n",
				path, len(cells), *cell)
			return exitUsage
		}
		targets = append(targets, *cell)
	default:
		for i, c := range cells {
			// Skip cells this executor does not claim rather than erroring: a real
			// notebook mixes languages, and running what you can is the useful
			// behaviour.
			if c.Lang == echoexec.Lang {
				targets = append(targets, i)
			}
		}
	}
	if len(targets) == 0 {
		fmt.Fprintf(stdout, "%s: no %s cells to run\n", path, echoexec.Lang)
		return exitOK
	}

	// Submit everything first, then poll. Submission returns immediately and runs
	// within a notebook are serialised, so this is the async loop in miniature
	// (harvest R3).
	ids := make([]run.ID, 0, len(targets))
	for _, i := range targets {
		id, err := s.Submit(path, i)
		if err != nil {
			fmt.Fprintf(stderr, "noterun: cell %d: %v\n", i, err)
			return exitProblem
		}
		ids = append(ids, id)
	}

	problems := 0
	deadline := time.Now().Add(*timeout)
	for _, id := range ids {
		r, ok := waitFor(s, id, deadline, signalled)
		if !ok {
			fmt.Fprintf(stderr, "noterun: run %s did not finish within %v\n", id, *timeout)
			return exitProblem
		}
		switch r.State {
		case run.Done:
			note := ""
			if r.Truncated {
				note = " (truncated)"
			}
			fmt.Fprintf(stdout, "cell %d %q: %s%s\n", r.CellIndex, r.Cell, r.Form, note)
		case run.Cancelled:
			fmt.Fprintf(stdout, "cell %d %q: cancelled\n", r.CellIndex, r.Cell)
		default:
			problems++
			fmt.Fprintf(stderr, "cell %d %q: %v\n", r.CellIndex, r.Cell, r.Err)
		}
	}

	if problems > 0 {
		return exitProblem
	}
	return exitOK
}

// waitFor polls a run to completion, which is the pattern the HTMX loop will use at
// M2 — state is pollable precisely so a caller never has to block on execution.
func waitFor(s *run.Scheduler, id run.ID, deadline time.Time, signalled <-chan struct{}) (run.Run, bool) {
	for {
		r, ok := s.State(id)
		if !ok {
			return run.Run{}, false
		}
		if r.State.Terminal() {
			return r, true
		}
		select {
		case <-signalled:
			// A signal shut the scheduler down; drained runs still reach a terminal
			// state, so loop once more rather than reporting a timeout.
			if r, ok := s.State(id); ok && r.State.Terminal() {
				return r, true
			}
		default:
		}
		if time.Now().After(deadline) {
			return r, false
		}
		time.Sleep(pollInterval)
	}
}
