package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const front = "---\nnotekit: 1\ntitle: T\n---\n\n"

// write creates a file under dir, making parent directories as needed.
func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// exec runs notefmt in-process and returns its exit code and streams.
func exec(args ...string) (code int, stdout, stderr string) {
	var out, errBuf bytes.Buffer
	code = run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestCheckCleanNotebook(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "notes.md", front+"## Disk usage\n\n```sh\ndu -h\n```\n\n```output\n1.2G\n```\n")

	code, stdout, stderr := exec("check", path)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "1 cell, 1 cell with results") {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestCheckRefusesNonNotebook(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name, content, want string
	}{
		{"no front matter", "## H\n\n```sh\na\n```\n", "missing YAML front matter"},
		{"no notekit key", "---\ntitle: T\n---\n", "no `notekit` key"},
		{"wrong version", "---\nnotekit: 2\n---\n", "not supported"},
		{"unterminated front matter", "---\nnotekit: 1\n", "unterminated front matter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := write(t, dir, tt.name+".md", tt.content)
			code, _, stderr := exec("check", path)
			// A refusal is a finding, not a crash: exit 1, not 2.
			if code != exitProblem {
				t.Fatalf("exit = %d, want %d\nstderr: %s", code, exitProblem, stderr)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr, tt.want)
			}
			if !strings.Contains(stderr, "1 error") {
				t.Errorf("stderr = %q, want an error summary", stderr)
			}
		})
	}
}

func TestCheckDuplicateID(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "dup.md", front+
		"## One\n\n```sh {id=k3m7q2vf}\na\n```\n\n## Two\n\n```sh {id=k3m7q2vf}\nb\n```\n")

	code, _, stderr := exec("check", path)
	if code != exitProblem {
		t.Fatalf("exit = %d, want %d", code, exitProblem)
	}
	if !strings.Contains(stderr, "duplicate cell id") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestCheckMalformedInfoStringIsAnError(t *testing.T) {
	dir := t.TempDir()
	// Duplicate keys in one info string are a tool error (§9), and notefmt was
	// asked to look at every cell.
	path := write(t, dir, "meta.md", front+"## H\n\n```sh {a=1, a=2}\na\n```\n")

	code, _, stderr := exec("check", path)
	if code != exitProblem {
		t.Fatalf("exit = %d, want %d", code, exitProblem)
	}
	if !strings.Contains(stderr, "duplicate key") {
		t.Errorf("stderr = %q", stderr)
	}
	// Reported with a line number so an editor can jump to it.
	if !strings.Contains(stderr, "meta.md:6") {
		t.Errorf("stderr = %q, want a file:line location", stderr)
	}
}

// TestCheckUnclosedFenceIsAWarning pins the distinction: the cell is readable and
// runnable, so this is not a format error, but its result can never be persisted.
func TestCheckUnclosedFenceIsAWarning(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "unclosed.md", front+"## H\n\n```sh\nno close\n")

	code, _, stderr := exec("check", path)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (a warning must not fail)\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stderr, "unclosed source fence") {
		t.Errorf("stderr = %q", stderr)
	}
	if !strings.Contains(stderr, "1 warning") {
		t.Errorf("stderr = %q, want a warning summary", stderr)
	}

	// -strict promotes warnings to problems, which is what CI wants.
	code, _, _ = exec("-strict", "check", path)
	if code != exitProblem {
		t.Errorf("with -strict: exit = %d, want %d", code, exitProblem)
	}
}

func TestCheckSeveralResultsIsAWarning(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "multi.md", front+
		"## H\n\n```sh\na\n```\n\n```output\none\n```\n\n```output\ntwo\n```\n")

	code, _, stderr := exec("check", path)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stderr, "2 result constructs") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestCheckSeveralFilesAccumulates(t *testing.T) {
	dir := t.TempDir()
	good := write(t, dir, "good.md", front+"## H\n\n```sh\na\n```\n")
	bad := write(t, dir, "bad.md", "not a notebook\n")

	code, stdout, stderr := exec("check", good, bad)
	if code != exitProblem {
		t.Fatalf("exit = %d, want %d", code, exitProblem)
	}
	// The good file is still reported: one bad file does not abort the run.
	if !strings.Contains(stdout, "good.md") {
		t.Errorf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "bad.md") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "notes.md", front+
		"## Disk usage\n\n```sh\ndu -h\n```\n\n```output\n1.2G\n```\n\n"+
		"## 日本語\n\n```cypher {id=k3m7q2vf}\nMATCH (n) RETURN n\n```\n")

	code, stdout, stderr := exec("list", path)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	for _, want := range []string{
		"LINE", "LANG", "ID", "SLUG", "RESULT", "HEADING",
		"disk-usage", "output", "Disk usage",
		"k3m7q2vf", "cypher", "日本語",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	// An empty slug and an absent id both render as "-", not as blanks.
	if !strings.Contains(stdout, "-") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestListNoCells(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "prose.md", front+"Just prose.\n")
	code, stdout, _ := exec("list", path)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "(no cells)") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestListMarksUnclosed(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "u.md", front+"## H\n\n```sh\nno close\n")
	_, stdout, _ := exec("list", path)
	if !strings.Contains(stdout, "(unclosed)") {
		t.Errorf("stdout = %q, want the cell flagged unclosed", stdout)
	}
}

// TestSidecars exercises all four classifications against a real directory.
func TestSidecars(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "notes.md", front+
		// Slug is "kept", id aaaa2345 — its artifact is current.
		"## Kept\n\n```cypher {id=aaaa2345}\na\n```\n\n"+
		"<!-- notekit:result kind=graph -->\n![Kept](notes.assets/kept--aaaa2345.png)\n\n"+
		// Slug is "renamed-heading", id bbbb2345 — its artifact is on disk under an
		// older slug, so it is stale and wants renaming.
		"## Renamed heading\n\n```cypher {id=bbbb2345}\nb\n```\n\n"+
		"<!-- notekit:result kind=graph -->\n![R](notes.assets/old-name--bbbb2345.png)\n")

	assets := filepath.Join(dir, "notes.assets")
	for _, name := range []string{
		"kept--aaaa2345.png",     // current
		"old-name--bbbb2345.png", // stale: heading was renamed
		"gone--cccc2345.png",     // orphan: no cell carries cccc2345
		"README.txt",             // foreign: no valid id
	} {
		write(t, assets, name, "x")
	}

	code, stdout, stderr := exec("sidecars", path)
	// Stale and orphan are warnings, so the exit code stays 0 without -strict.
	if code != exitOK {
		t.Fatalf("exit = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}

	for _, want := range []string{
		"kept--aaaa2345.png", "current",
		"old-name--bbbb2345.png", "stale", "renamed-heading--bbbb2345.png",
		"gone--cccc2345.png", "orphan",
		"README.txt", "foreign",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	// A rename is not a lost artifact, and an orphan is never deleted.
	if !strings.Contains(stderr, "rename this file") {
		t.Errorf("stderr = %q, want a rename suggestion", stderr)
	}
	if !strings.Contains(stderr, "the cell was deleted") {
		t.Errorf("stderr = %q, want the orphan explained", stderr)
	}

	// Every file must still be on disk: notefmt reports, it does not repair.
	for _, name := range []string{"kept--aaaa2345.png", "old-name--bbbb2345.png", "gone--cccc2345.png", "README.txt"} {
		if _, err := os.Stat(filepath.Join(assets, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
}

func TestSidecarsNoDirectory(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "notes.md", front+"## H\n\n```sh\na\n```\n")
	code, stdout, stderr := exec("sidecars", path)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "(no sidecar directory)") {
		t.Errorf("stdout = %q", stdout)
	}
}

// TestCheckReportsSidecarProblems: check covers sidecars too, so a single
// `notefmt check` in CI catches a stale or orphaned artifact.
func TestCheckReportsSidecarProblems(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "notes.md", front+"## Kept\n\n```cypher {id=aaaa2345}\na\n```\n")
	write(t, filepath.Join(dir, "notes.assets"), "gone--cccc2345.png", "x")

	code, _, stderr := exec("check", path)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stderr, "orphan") {
		t.Errorf("stderr = %q, want the orphan reported by check", stderr)
	}
	code, _, _ = exec("-strict", "check", path)
	if code != exitProblem {
		t.Errorf("with -strict: exit = %d, want %d", code, exitProblem)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"command with no files", []string{"check"}},
		{"unknown command", []string{"frobnicate", "x.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := exec(tt.args...)
			if code != exitUsage {
				t.Errorf("exit = %d, want %d", code, exitUsage)
			}
			if !strings.Contains(stderr, "usage:") {
				t.Errorf("stderr = %q, want usage text", stderr)
			}
		})
	}
}

func TestHelp(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		code, stdout, _ := exec(arg)
		if code != exitOK {
			t.Errorf("%s: exit = %d, want %d", arg, code, exitOK)
		}
		if !strings.Contains(stdout, "usage:") {
			t.Errorf("%s: stdout = %q", arg, stdout)
		}
	}
}

func TestMissingFileIsAnIOFailure(t *testing.T) {
	// An unreadable path is the operator's mistake, not a finding about a
	// notebook, so it exits 2 rather than 1.
	code, _, stderr := exec("check", filepath.Join(t.TempDir(), "nope.md"))
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "notefmt:") {
		t.Errorf("stderr = %q", stderr)
	}
}

// TestCheckCorpus is part of the M0 gate: notefmt round-trip-checks every corpus
// file, and the only findings are the ones those files exist to demonstrate.
func TestCheckCorpus(t *testing.T) {
	paths, err := filepath.Glob("../../doc/testdata/corpus/*.md")
	if err != nil || len(paths) == 0 {
		t.Fatalf("globbing corpus: %v (%d files)", err, len(paths))
	}

	code, stdout, stderr := exec(append([]string{"check"}, paths...)...)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d — the corpus must be free of errors\nstderr: %s", code, exitOK, stderr)
	}
	if strings.Contains(stderr, "error:") {
		t.Errorf("corpus produced errors:\n%s", stderr)
	}
	for _, p := range paths {
		if !strings.Contains(stdout, filepath.Base(p)) {
			t.Errorf("no report for %s", p)
		}
	}
	// The expected warnings, and no others: two multi-result cells and one
	// unclosed fence.
	if got, want := strings.Count(stderr, "warning:"), 3; got != want {
		t.Errorf("got %d warnings, want %d:\n%s", got, want, stderr)
	}
}

// TestSummariseBothCounts covers the report line when a run produces errors and
// warnings together, which is the normal case for a messy notebook.
func TestSummariseBothCounts(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "messy.md", front+
		"## Bad metadata\n\n```sh {a=1, a=2}\na\n```\n\n"+ // error
		"## Unclosed\n\n```sh\nno close\n") // warning

	code, _, stderr := exec("check", path)
	if code != exitProblem {
		t.Fatalf("exit = %d, want %d", code, exitProblem)
	}
	if !strings.Contains(stderr, "1 error, 1 warning") {
		t.Errorf("stderr = %q, want a combined summary", stderr)
	}
}

// TestCheckMalformedResultMetadata: a result block's own info string is checked too,
// not just the source fence's.
func TestCheckMalformedResultMetadata(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "r.md", front+
		"## H\n\n```sh\na\n```\n\n```output {run=\"x\", run=\"y\"}\nr\n```\n")

	code, _, stderr := exec("check", path)
	if code != exitProblem {
		t.Fatalf("exit = %d, want %d\nstderr: %s", code, exitProblem, stderr)
	}
	if !strings.Contains(stderr, "output result metadata") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestSidecarsOnNonNotebook(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "bad.md", "not a notebook\n")
	code, _, stderr := exec("sidecars", path)
	if code != exitProblem {
		t.Fatalf("exit = %d, want %d", code, exitProblem)
	}
	if !strings.Contains(stderr, "not a notekit notebook") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestListOnNonNotebook(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "bad.md", "not a notebook\n")
	code, _, stderr := exec("list", path)
	if code != exitProblem {
		t.Fatalf("exit = %d, want %d", code, exitProblem)
	}
	if !strings.Contains(stderr, "not a notekit notebook") {
		t.Errorf("stderr = %q", stderr)
	}
}
