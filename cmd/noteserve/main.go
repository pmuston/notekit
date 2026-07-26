// Command noteserve serves a notekit notebook in the browser.
//
// It is the M2 demo: the full HTMX loop — run, poll with a spinner, render results,
// edit prose, save — driven by the echo executor. It is also the shape a real tool's
// `main` takes, so clinote v2 at M3 should read much like this with a shell executor
// swapped in.
//
//	noteserve notebook.md              serve on the default address
//	noteserve -addr :9000 notebook.md  serve elsewhere
//
// Exit status is 0 on a clean shutdown, 1 if the server failed, and 2 for a usage or
// I/O failure.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/pmuston/notekit/exec/echoexec"
	"github.com/pmuston/notekit/run"
	"github.com/pmuston/notekit/serve"
)

const (
	exitOK      = 0
	exitProblem = 1
	exitUsage   = 2
)

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("noteserve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	poll := fs.Duration("poll", serve.DefaultPollInterval, "how often the browser polls a running cell")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: noteserve [flags] <notebook.md>\n\nflags:\n")
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
	sched := run.New(run.WithTool("noteserve/0.1"))

	// The kit owns signal handling, so a Ctrl-C destroys every live session rather
	// than leaking whatever engine sat behind it.
	signalled, stopSignals := sched.HandleSignals(stderr)
	defer stopSignals()

	if err := sched.Open(ctx, path, echoexec.New()); err != nil {
		fmt.Fprintf(stderr, "noteserve: %v\n", err)
		return exitUsage
	}
	defer func() { _ = sched.Shutdown(ctx) }()

	srv, err := serve.New(sched, path, serve.WithPollInterval(*poll))
	if err != nil {
		fmt.Fprintf(stderr, "noteserve: %v\n", err)
		return exitUsage
	}

	e := srv.Echo()
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           e,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		fmt.Fprintf(stdout, "noteserve: http://%s  (%s)\n", *addr, path)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		if err != nil {
			fmt.Fprintf(stderr, "noteserve: %v\n", err)
			return exitProblem
		}
		return exitOK
	case <-signalled:
		// Sessions are already destroyed by the time this fires; drain the HTTP
		// server so an in-flight request is not cut off mid-response.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(stderr, "noteserve: %v\n", err)
			return exitProblem
		}
		fmt.Fprintln(stdout, "noteserve: stopped")
		return exitOK
	}
}
