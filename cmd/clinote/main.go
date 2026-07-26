//go:build unix

// Command clinote is a shell notebook: a notekit notebook whose cells are shell
// commands, run in one persistent shell and served in the browser.
//
// This is clinote v2, and the whole binary is an executor plus a `main`. Every other
// concern — the on-disk format, byte-range splice, run scheduling, capture limits,
// signal-safe teardown, the HTMX UI — belongs to the kit. All pty knowledge lives in
// shell.go and in no kit package.
//
//	clinote                    pick the notebook in the current directory
//	clinote notebook.md        serve that notebook
//	clinote new notebook.md    create a notebook, then serve it
//	clinote -list              list candidate notebooks and exit
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
	"path/filepath"
	"time"

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/internal/notetool"
	"github.com/pmuston/notekit/run"
	"github.com/pmuston/notekit/serve"
)

// version is the provenance value written into every result block as `tool` (§6).
const version = "clinote/2.0"

const (
	exitOK      = 0
	exitProblem = 1
	exitUsage   = 2
)

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	// Strip the subcommand before parsing, so `new` accepts exactly the same flags as a
	// plain invocation and there is only one flagset to keep in step.
	sub := ""
	if len(args) > 0 && args[0] == "new" {
		sub, args = "new", args[1:]
	}

	fs := flag.NewFlagSet("clinote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	shell := fs.String("shell", defaultShell(), "shell to run cells in (bash or zsh)")
	// TERM=dumb is clinote v1's proven choice: it stops the shell emitting
	// cursor-positioning sequences that would land in a cell's captured output. Set a
	// real terminal type to let tools auto-detect colour, which the UI renders live
	// and the format strips from disk.
	term := fs.String("term", "dumb", "TERM for the shell; a real value enables colour auto-detection")
	poll := fs.Duration("poll", serve.DefaultPollInterval, "how often the browser polls a running cell")
	list := fs.Bool("list", false, "list candidate notebooks and exit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: clinote [flags] [notebook.md]\n"+
			"       clinote new [flags] <notebook.md>\n\n"+
			"With no notebook, clinote uses the one in the current directory.\n"+
			"`new` writes a notebook with one starter cell, then serves it.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 1 {
		// `clinote -addr X new n.md` parses as two paths, because flag stops at the
		// first non-flag argument. Say so, rather than printing usage and leaving the
		// user to spot that the subcommand has to come first.
		for _, a := range args {
			if a == "new" {
				fmt.Fprintf(stderr, "clinote: `new` must come first: "+
					"clinote new [flags] <notebook.md>\n")
				return exitUsage
			}
		}
		fs.Usage()
		return exitUsage
	}

	if *list {
		found, err := notetool.FindNotebooks(".")
		if err != nil {
			fmt.Fprintf(stderr, "clinote: %v\n", err)
			return exitUsage
		}
		if len(found) == 0 {
			fmt.Fprintln(stdout, "no notekit notebooks in the current directory")
			return exitOK
		}
		for _, p := range found {
			fmt.Fprintln(stdout, p)
		}
		return exitOK
	}

	ex, err := NewShellExecutor(*shell, *term, doc.OutputCap)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
	}

	var path string
	if sub == "new" {
		if fs.NArg() != 1 {
			fmt.Fprintf(stderr, "clinote: `new` needs exactly one path\n")
			return exitUsage
		}
		path = fs.Arg(0)
		if err := notetool.Create(path, "clinote", starterCell(ex.Lang())); err != nil {
			fmt.Fprintf(stderr, "clinote: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(stdout, "clinote: created %s\n", path)
	} else {
		path, err = notetool.Resolve(fs.Arg(0), ".")
		if err != nil {
			fmt.Fprintf(stderr, "clinote: %v\n", err)
			return exitUsage
		}
		// A refusal stops us; a warning is said and stepped over. The advisory tool key
		// must never decide whether a notebook opens (§2.1).
		warn, err := notetool.Inspect(path, "clinote", ex.Lang())
		if err != nil {
			fmt.Fprintf(stderr, "clinote: %v\n", err)
			return exitUsage
		}
		if warn != "" {
			fmt.Fprintf(stderr, "clinote: warning: %s\n", warn)
		}
	}

	ctx := context.Background()
	sched := run.New(run.WithTool(version))

	// The kit owns signal handling, so a Ctrl-C destroys the shell rather than
	// leaving a pty child behind.
	signalled, stopSignals := sched.HandleSignals(stderr)
	defer stopSignals()

	if err := sched.Open(ctx, path, ex); err != nil {
		fmt.Fprintf(stderr, "clinote: %v\n", err)
		return exitUsage
	}
	defer func() { _ = sched.Shutdown(ctx) }()

	srv, err := serve.New(sched, path, serve.WithPollInterval(*poll))
	if err != nil {
		fmt.Fprintf(stderr, "clinote: %v\n", err)
		return exitUsage
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Echo(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		fmt.Fprintf(stdout, "clinote: http://%s  (%s, %s)\n", *addr, path, *shell)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		if err != nil {
			fmt.Fprintf(stderr, "clinote: %v\n", err)
			return exitProblem
		}
		return exitOK
	case <-signalled:
		// Sessions are already destroyed; drain the HTTP server so an in-flight
		// request is not cut off mid-response.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(stderr, "clinote: %v\n", err)
			return exitProblem
		}
		fmt.Fprintln(stdout, "clinote: stopped")
		return exitOK
	}
}

// defaultShell picks the user's shell when it is one clinote supports.
func defaultShell() string {
	base := filepath.Base(os.Getenv("SHELL"))
	switch base {
	case "bash", "zsh":
		return base
	}
	return "bash"
}

// filepathDir is a tiny indirection so shell.go need not import path/filepath.
func filepathDir(path string) string { return filepath.Dir(path) }

// starterCell is the cell `new` writes. A new notebook gets one cell of the tool's own
// language and nothing else — no invented prose, no placeholder result (§10 f).
//
// It is not merely a courtesy. A notebook's engine is derived from the info-string tags its
// cells carry (§2.1), so a notebook with no cells has nothing to derive from. The starter
// cell is what makes every notebook's engine knowable from the moment it is created, which
// is why `new` writes one rather than an empty file.
func starterCell(lang string) doc.NewCell {
	return doc.NewCell{
		Heading: "First command",
		Lang:    lang,
		Body:    "echo \"hello from clinote\"\n",
	}
}
