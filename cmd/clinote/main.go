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
	"sort"
	"strings"
	"time"

	"github.com/pmuston/notekit/doc"
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
		found, err := findNotebooks(".")
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
		if err := createNotebook(path, ex.Lang()); err != nil {
			fmt.Fprintf(stderr, "clinote: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(stdout, "clinote: created %s\n", path)
	} else {
		path, err = resolveNotebook(fs.Arg(0))
		if err != nil {
			fmt.Fprintf(stderr, "clinote: %v\n", err)
			return exitUsage
		}
		if err := checkEngine(path, ex.Lang()); err != nil {
			fmt.Fprintf(stderr, "clinote: %v\n", err)
			return exitUsage
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

// resolveNotebook returns the notebook to serve, using the picker when no path is given.
//
// The picker is deliberately not interactive: with exactly one candidate the answer is
// obvious, and with several the useful thing is to name them and let the user choose,
// rather than guess and open the wrong notebook.
func resolveNotebook(arg string) (string, error) {
	if arg != "" {
		if err := checkNotebook(arg); err != nil {
			return "", err
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

// checkNotebook reports why a path cannot be served, in the terms the user can act on.
func checkNotebook(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := doc.Parse(src); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// findNotebooks lists the notekit notebooks in a directory, in name order.
//
// A file is a candidate only if it actually parses: refusing to guess is the format's
// posture (§2), and offering a file that turns out not to be a notebook would move the
// error to a worse place.
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

// createNotebook writes a new notebook at path.
//
// An existing file is never touched. The file is the artifact, so overwriting one on a
// mistyped path would destroy work that no tool can recover — and refusing costs the user
// one command.
func createNotebook(path, lang string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	src, err := doc.Scaffold(titleFromPath(path), starterCell(lang))
	if err != nil {
		return err
	}
	// The same atomic write every other durable write uses: a reader must never see a
	// half-written notebook.
	return doc.WriteFileAtomic(path, src, 0o644)
}

// titleFromPath derives a readable title from a filename, so `clinote new parts-list.md`
// opens as "Parts list" rather than "parts-list".
func titleFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	base = strings.Join(strings.Fields(base), " ")
	if base == "" {
		return ""
	}
	return strings.ToUpper(base[:1]) + base[1:]
}

// checkEngine refuses a notebook this binary cannot run, before the server starts.
//
// The cells already say which engine a notebook wants, so there is nothing to look up and
// nothing that can disagree (§2.1). Without this the mismatch surfaced only when someone
// clicked Run, once per cell, as a message written for a developer — the notebook opened,
// rendered and looked ready.
//
// A notebook with no cells is allowed: there is nothing to contradict, and a `new` notebook
// always has one. So is a notebook where only *some* cells match, since package run checks
// each cell as it runs it; refusing the whole file would be stricter than the format.
func checkEngine(path, lang string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	nb, err := doc.Parse(src)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	langs := nb.Langs()
	if len(langs) == 0 {
		return nil
	}
	for _, l := range langs {
		if l == lang {
			return nil
		}
	}
	msg := fmt.Sprintf("%s has %s cells, and clinote runs %q cells",
		path, quoteList(langs), lang)
	if hint := toolFor(langs); hint != "" {
		msg += "\n  try: " + hint + " " + path
	}
	return errors.New(msg)
}

// toolFor names the sibling tool for a set of languages, so the error can point somewhere
// instead of only saying no.
//
// Deliberately a short list rather than a registry: executors are compiled in, so a tool
// can only ever know about the siblings that existed when it was built. If a third notebook
// tool lands, this is worth extracting into one place rather than growing here.
func toolFor(langs []string) string {
	for _, l := range langs {
		if l == "sql" {
			return "sqlnote"
		}
	}
	return ""
}

// quoteList renders a language list for an error message.
func quoteList(langs []string) string {
	quoted := make([]string, len(langs))
	for i, l := range langs {
		quoted[i] = fmt.Sprintf("%q", l)
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}
