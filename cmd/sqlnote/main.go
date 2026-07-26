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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pmuston/notekit/doc"
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
	fs := flag.NewFlagSet("sqlnote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	maxRows := fs.Int("max-rows", DefaultMaxRows, "maximum rows one cell may return")
	poll := fs.Duration("poll", serve.DefaultPollInterval, "how often the browser polls a running cell")
	list := fs.Bool("list", false, "list candidate notebooks and exit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: sqlnote [flags] [notebook.md]\n\n"+
			"With no notebook, sqlnote uses the one in the current directory.\n"+
			"The database comes from the notebook's `%s` front-matter key;\n"+
			"without it the notebook is self-contained and runs in memory.\n\nflags:\n",
			FrontKeyDB)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return exitUsage
	}

	if *list {
		found, err := findNotebooks(".")
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

	path, err := resolveNotebook(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "sqlnote: %v\n", err)
		return exitUsage
	}

	ex, err := NewExecutor(*maxRows)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
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

// resolveNotebook returns the notebook to serve, using the picker when no path is given.
//
// Deliberately not interactive: with one candidate the answer is obvious, and with several
// the useful thing is to name them rather than open the wrong one.
func resolveNotebook(arg string) (string, error) {
	if arg != "" {
		src, err := os.ReadFile(arg)
		if err != nil {
			return "", err
		}
		if _, err := doc.Parse(src); err != nil {
			return "", fmt.Errorf("%s: %w", arg, err)
		}
		return arg, nil
	}

	found, err := findNotebooks(".")
	if err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", errors.New("no notekit notebooks in the current directory; " +
			"name one, or create a file with `notekit: 1` front matter")
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("several notebooks here — name one of:\n  %s",
			strings.Join(found, "\n  "))
	}
}

// findNotebooks lists the notekit notebooks in a directory, in name order.
//
// A file is a candidate only if it actually parses: refusing to guess is the format's
// posture (§2), and offering a file that turns out not to be a notebook would move the
// error somewhere worse.
func findNotebooks(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if _, err := doc.Parse(src); err != nil {
			continue
		}
		found = append(found, p)
	}
	sort.Strings(found)
	return found, nil
}
