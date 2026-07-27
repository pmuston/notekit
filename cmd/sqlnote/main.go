// Command sqlnote is a SQL notebook: a notekit notebook whose cells are SQL statements,
// run against one SQLite connection and served in the browser.
//
// It is the kit's second consumer and the point of harvest validation gate 4 — the first
// non-shell domain, and so the first real test of whether the exec boundary abstracts
// anything. The whole binary is an executor plus a `main`; all database knowledge lives in
// sql.go and in no kit package.
//
//	sqlnote                    pick the notebook in the current directory
//	sqlnote notebook.md        serve that notebook
//	sqlnote new notebook.md    create a notebook, then serve it
//	sqlnote -list              list candidate notebooks and exit
//
// The database comes from the notebook's own front matter (§2 passthrough):
//
//	---
//	notekit: 1
//	sqlnote-db: ./analysis.db
//	---
//
// Omit it and the notebook is self-contained — its cells build the data in memory, and a
// run from empty reconstructs it. Name a file and the notebook is bound to that store
// instead. Those are harvest R2's two data modes, which turn out to be tool configuration
// rather than anything the format needs to know.
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

	"github.com/pmuston/notekit/doc"
	"github.com/pmuston/notekit/internal/siblings"
	"github.com/pmuston/notekit/notetool"
	"github.com/pmuston/notekit/run"
	"github.com/pmuston/notekit/serve"
)

// version is the provenance value written into every result block as `tool` (§6).
const version = "sqlnote/1.0"

// DefaultMaxRows bounds how many rows one cell may return, so a `SELECT *` on a large
// table cannot exhaust memory before the runtime sees a byte.
const DefaultMaxRows = 10_000

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

	fs := flag.NewFlagSet("sqlnote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	maxRows := fs.Int("max-rows", DefaultMaxRows, "maximum rows one cell may return")
	poll := fs.Duration("poll", serve.DefaultPollInterval, "how often the browser polls a running cell")
	list := fs.Bool("list", false, "list candidate notebooks and exit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: sqlnote [flags] [notebook.md]\n"+
			"       sqlnote new [flags] <notebook.md>\n\n"+
			"With no notebook, sqlnote uses the one in the current directory.\n"+
			"`new` writes a notebook with one starter cell, then serves it.\n"+
			"The database comes from the notebook's `%s` front-matter key;\n"+
			"without it the notebook is self-contained and runs in memory.\n\nflags:\n",
			FrontKeyDB)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 1 {
		// `sqlnote -addr X new n.md` parses as two paths, because flag stops at the
		// first non-flag argument. Say so, rather than printing usage and leaving the
		// user to spot that the subcommand has to come first.
		for _, a := range args {
			if a == "new" {
				fmt.Fprintf(stderr, "sqlnote: `new` must come first: "+
					"sqlnote new [flags] <notebook.md>\n")
				return exitUsage
			}
		}
		fs.Usage()
		return exitUsage
	}

	if *list {
		found, err := notetool.FindNotebooks(".")
		if err != nil {
			fmt.Fprintf(stderr, "sqlnote: %v\n", err)
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

	ex, err := NewExecutor(*maxRows)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
	}

	// What this binary is, and what it knows of its siblings. Peers comes from the module
	// rather than from the kit: notetool names no tools of its own.
	self := notetool.Tool{Name: "sqlnote", Lang: ex.Lang(), Peers: siblings.All}

	var path string
	if sub == "new" {
		if fs.NArg() != 1 {
			fmt.Fprintf(stderr, "sqlnote: `new` needs exactly one path\n")
			return exitUsage
		}
		path = fs.Arg(0)
		if err := self.Create(path, starterCell(ex.Lang())); err != nil {
			fmt.Fprintf(stderr, "sqlnote: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(stdout, "sqlnote: created %s\n", path)
	} else {
		path, err = notetool.Resolve(fs.Arg(0), ".")
		if err != nil {
			fmt.Fprintf(stderr, "sqlnote: %v\n", err)
			return exitUsage
		}
		// A refusal stops us; a warning is said and stepped over. The advisory tool key
		// must never decide whether a notebook opens (§2.1).
		warn, err := self.Inspect(path)
		if err != nil {
			fmt.Fprintf(stderr, "sqlnote: %v\n", err)
			return exitUsage
		}
		if warn != "" {
			fmt.Fprintf(stderr, "sqlnote: warning: %s\n", warn)
		}
	}

	ctx := context.Background()
	sched := run.New(run.WithTool(version))

	// The kit owns signal handling, so a Ctrl-C closes the database rather than
	// leaving a connection open.
	signalled, stopSignals := sched.HandleSignals(stderr)
	defer stopSignals()

	if err := sched.Open(ctx, path, ex); err != nil {
		fmt.Fprintf(stderr, "sqlnote: %v\n", err)
		return exitUsage
	}
	defer func() { _ = sched.Shutdown(ctx) }()

	srv, err := serve.New(sched, path, serve.WithPollInterval(*poll))
	if err != nil {
		fmt.Fprintf(stderr, "sqlnote: %v\n", err)
		return exitUsage
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Echo(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		fmt.Fprintf(stdout, "sqlnote: http://%s  (%s, %s)\n", *addr, path, describeDB(path))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		if err != nil {
			fmt.Fprintf(stderr, "sqlnote: %v\n", err)
			return exitProblem
		}
		return exitOK
	case <-signalled:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(stderr, "sqlnote: %v\n", err)
			return exitProblem
		}
		fmt.Fprintln(stdout, "sqlnote: stopped")
		return exitOK
	}
}

// describeDB reports which database a notebook names, for the startup line. Saying "in
// memory" out loud matters: a self-contained notebook discards its data on exit, and a
// user should not discover that afterwards.
func describeDB(path string) string {
	src, err := os.ReadFile(path)
	if err != nil {
		return "unknown database"
	}
	nb, err := doc.Parse(src)
	if err != nil {
		return "unknown database"
	}
	if db := nb.Front()[FrontKeyDB]; db != "" {
		return "db " + db
	}
	return "in memory, self-contained"
}

// starterCell is the cell `new` writes: one cell of the tool's own language and nothing
// else — no invented prose, no placeholder result (§10 f).
//
// It is not merely a courtesy. A notebook's engine is derived from the info-string tags its
// cells carry (§2.1), so a notebook with no cells has nothing to derive from. The starter
// cell is what makes every notebook's engine knowable from the moment it is created.
//
// The query stands alone rather than reading a table, because a new notebook has no schema
// yet and a starter cell that errors on first run would be a poor greeting.
func starterCell(lang string) doc.NewCell {
	return doc.NewCell{
		Heading: "First query",
		Lang:    lang,
		Body:    "SELECT 'hello from sqlnote' AS greeting;\n",
	}
}
