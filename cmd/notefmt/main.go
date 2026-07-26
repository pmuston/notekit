// Command notefmt inspects and lints notekit notebooks.
//
// It is the M0 deliverable and stays useful permanently: a linter for CI, and the
// debugging tool for every later stage of the kit.
//
//	notefmt check    notebook.md...   round-trip, format errors, sidecar state
//	notefmt list     notebook.md...   one line per cell
//	notefmt sidecars notebook.md...   classify the sidecar directory
//
// Exit status is 0 when nothing is wrong, 1 when a problem was found, and 2 for a
// usage or I/O failure — the conventional split for a linter, so `notefmt check` can
// gate a commit.
//
// notefmt never writes to a notebook. Reporting and repairing are different jobs, and
// only the second needs a tool that can damage a file.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/pmuston/notekit/doc"
)

const usage = `notefmt inspects and lints notekit notebooks.

usage:
  notefmt check    <notebook.md>...   round-trip, format errors, sidecar state
  notefmt list     <notebook.md>...   one line per cell
  notefmt sidecars <notebook.md>...   classify the sidecar directory

flags:
  -strict   treat warnings as problems too (exit 1)

exit status:
  0  nothing wrong
  1  a problem was found
  2  usage or I/O failure
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// exit codes
const (
	exitOK      = 0
	exitProblem = 1
	exitUsage   = 2
)

func run(args []string, stdout, stderr io.Writer) int {
	var strict bool
	var rest []string
	for _, a := range args {
		switch a {
		case "-strict", "--strict":
			strict = true
		case "-h", "--help", "help":
			fmt.Fprint(stdout, usage)
			return exitOK
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) < 2 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	cmd, paths := rest[0], rest[1:]
	var fn func(string, *report, io.Writer) error
	switch cmd {
	case "check":
		fn = checkFile
	case "list":
		fn = listFile
	case "sidecars":
		fn = sidecarsFile
	default:
		fmt.Fprintf(stderr, "notefmt: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}

	rep := &report{w: stderr}
	for _, path := range paths {
		if err := fn(path, rep, stdout); err != nil {
			fmt.Fprintf(stderr, "notefmt: %v\n", err)
			return exitUsage
		}
	}

	rep.summarise(stderr)
	if rep.errors > 0 || (strict && rep.warnings > 0) {
		return exitProblem
	}
	return exitOK
}

// report accumulates findings across files.
type report struct {
	w        io.Writer
	errors   int
	warnings int
}

func (r *report) errorf(loc string, format string, a ...any) {
	r.errors++
	fmt.Fprintf(r.w, "%s: error: %s\n", loc, fmt.Sprintf(format, a...))
}

func (r *report) warnf(loc string, format string, a ...any) {
	r.warnings++
	fmt.Fprintf(r.w, "%s: warning: %s\n", loc, fmt.Sprintf(format, a...))
}

func (r *report) summarise(w io.Writer) {
	switch {
	case r.errors > 0 && r.warnings > 0:
		fmt.Fprintf(w, "%s, %s\n", plural(r.errors, "error"), plural(r.warnings, "warning"))
	case r.errors > 0:
		fmt.Fprintf(w, "%s\n", plural(r.errors, "error"))
	case r.warnings > 0:
		fmt.Fprintf(w, "%s\n", plural(r.warnings, "warning"))
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// openNotebook reads and parses a notebook, reporting a refusal as an error rather
// than as an I/O failure: a non-notekit file is a finding, not a crash.
//
// It returns a nil Notebook with a nil error when the file was refused, so callers
// continue to the next file.
func openNotebook(path string, rep *report) (*doc.Notebook, []byte, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	n, err := doc.Parse(src)
	if err != nil {
		var notNotebook *doc.NotNotebookError
		var dupID *doc.DuplicateIDError
		switch {
		case errors.As(err, &notNotebook), errors.As(err, &dupID):
			rep.errorf(path, "%v", err)
			return nil, src, nil
		default:
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return n, src, nil
}

func checkFile(path string, rep *report, stdout io.Writer) error {
	n, src, err := openNotebook(path, rep)
	if err != nil || n == nil {
		return err
	}

	// Round-trip identity (§10). Applying no edits must reproduce the file.
	out, applyErr := n.Apply()
	if applyErr != nil {
		rep.errorf(path, "applying no edits failed: %v", applyErr)
	} else if string(out) != string(src) {
		rep.errorf(path, "round trip is not byte-identical")
	}

	results := 0
	for _, c := range n.Cells() {
		loc := fmt.Sprintf("%s:%d", path, n.Line(c.Heading.Start))

		// A malformed info string is a tool error (§9), and notefmt has been asked
		// to look at every cell, so it reports all of them.
		if c.MetaErr != nil {
			rep.errorf(loc, "cell %q: %v", c.HeadingText, c.MetaErr)
		}
		// Not a format error: the cell is readable and runnable. But its result can
		// never be persisted, so it is worth saying out loud (§4.2).
		if !c.Closed {
			rep.warnf(loc, "cell %q has an unclosed source fence, so its result cannot be persisted",
				c.HeadingText)
		}
		if len(c.Results) > 1 {
			// Legal on read; the next run collapses them to one (§4.2).
			rep.warnf(loc, "cell %q has %d result constructs; a run will replace them with one",
				c.HeadingText, len(c.Results))
		}
		if len(c.Results) > 0 {
			results++
		}
		for _, r := range c.Results {
			if r.MetaErr != nil {
				rep.errorf(fmt.Sprintf("%s:%d", path, n.Line(r.Span.Start)),
					"cell %q: %s result metadata: %v", c.HeadingText, r.Form, r.MetaErr)
			}
		}
	}

	if err := checkSidecars(path, n, rep, nil); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "%s: %s, %s with results\n",
		path, plural(len(n.Cells()), "cell"), plural(results, "cell"))
	return nil
}

func listFile(path string, rep *report, stdout io.Writer) error {
	n, _, err := openNotebook(path, rep)
	if err != nil || n == nil {
		return err
	}

	fmt.Fprintf(stdout, "%s\n", path)
	if len(n.Cells()) == 0 {
		fmt.Fprintf(stdout, "  (no cells)\n")
		return nil
	}

	tw := tabwriter.NewWriter(stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "  LINE\tLANG\tID\tSLUG\tRESULT\tHEADING")
	for _, c := range n.Cells() {
		id := c.ID
		if id == "" {
			id = "-"
		}
		slug := c.Slug
		if slug == "" {
			slug = "-"
		}
		result := "none"
		if len(c.Results) > 0 {
			result = c.Results[0].Form.String()
			if len(c.Results) > 1 {
				result = fmt.Sprintf("%s+%d", result, len(c.Results)-1)
			}
		}
		flags := ""
		if !c.Closed {
			flags = " (unclosed)"
		}
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\t%s%s\n",
			n.Line(c.Heading.Start), c.Lang, id, slug, result, c.HeadingText, flags)
	}
	return tw.Flush()
}

func sidecarsFile(path string, rep *report, stdout io.Writer) error {
	n, _, err := openNotebook(path, rep)
	if err != nil || n == nil {
		return err
	}
	dir := doc.SidecarDir(path)
	fmt.Fprintf(stdout, "%s -> %s\n", path, dir)
	return checkSidecars(path, n, rep, stdout)
}

// checkSidecars classifies the notebook's sidecar directory (§8.1). When stdout is
// nil only problems are reported; otherwise every file is listed.
func checkSidecars(path string, n *doc.Notebook, rep *report, stdout io.Writer) error {
	dir := doc.SidecarDir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			if stdout != nil {
				fmt.Fprintf(stdout, "  (no sidecar directory)\n")
			}
			return nil
		}
		return fmt.Errorf("reading %s: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var tw *tabwriter.Writer
	if stdout != nil {
		tw = tabwriter.NewWriter(stdout, 2, 8, 2, ' ', 0)
	}

	for _, s := range doc.ClassifySidecars(names, n.Cells()) {
		rel := filepath.Join(dir, s.Name)
		switch s.State {
		case doc.SidecarStale:
			// The id matched, so this is a rename, not a lost artifact (§8.1).
			rep.warnf(rel, "heading renamed; rename this file to %q", s.Want)
		case doc.SidecarOrphan:
			// Now means exactly one thing: the cell was deleted. Never deleted
			// automatically (§8).
			rep.warnf(rel, "orphan: no cell carries id %q (the cell was deleted)", s.ID)
		}
		if tw != nil {
			detail := ""
			switch s.State {
			case doc.SidecarStale:
				detail = "-> " + s.Want
			case doc.SidecarCurrent:
				detail = s.Cell.HeadingText
			case doc.SidecarOrphan:
				detail = "id " + s.ID
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", s.Name, s.State, detail)
		}
	}
	if tw != nil {
		return tw.Flush()
	}
	return nil
}
