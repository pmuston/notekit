package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmuston/notekit/doc"
)

const front = "---\nnotekit: 1\ntitle: T\n---\n\n"

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func exec(args ...string) (code int, stdout, stderr string) {
	var out, errBuf bytes.Buffer
	code = runMain(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// TestRunsEveryEchoCell is the M1 gate: the full async loop driven end to end by the
// echo executor, persisting every result form.
func TestRunsEveryEchoCell(t *testing.T) {
	path := write(t, front+
		"Prose that no tool touches.\n\n"+
		"## Plain text\n\n```echo\nhello\n```\n\n"+
		"## A table\n\n```echo {format=csv}\na,b\n1,2\n```\n\n"+
		"## A failure\n\n```echo {status=127}\nnot found\n```\n\n"+
		"## Another language\n\n```sql\nSELECT 1\n```\n")

	code, stdout, stderr := exec(path)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d\nstderr: %s", code, exitOK, stderr)
	}
	for _, want := range []string{`cell 0 "Plain text": output`, `cell 1 "A table": output`, `cell 2 "A failure": error`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}

	got := read(t, path)
	nb, err := doc.Parse([]byte(got))
	if err != nil {
		t.Fatalf("the written notebook does not parse: %v", err)
	}
	cells := nb.Cells()
	if len(cells) != 4 {
		t.Fatalf("got %d cells, want 4", len(cells))
	}

	wantForms := []doc.ResultForm{doc.ResultOutput, doc.ResultOutput, doc.ResultError}
	for i, want := range wantForms {
		if len(cells[i].Results) != 1 {
			t.Fatalf("cell %d has %d results, want 1", i, len(cells[i].Results))
		}
		if got := cells[i].Results[0].Form; got != want {
			t.Errorf("cell %d form = %v, want %v", i, got, want)
		}
	}
	// A cell this executor does not claim is skipped, not failed: a real notebook
	// mixes languages.
	if len(cells[3].Results) != 0 {
		t.Errorf("the sql cell should have been skipped, got %d results", len(cells[3].Results))
	}
	if !strings.Contains(got, "Prose that no tool touches.\n") {
		t.Error("prose was lost")
	}
}

func TestRunOneCell(t *testing.T) {
	path := write(t, front+
		"## First\n\n```echo\none\n```\n\n"+
		"## Second\n\n```echo\ntwo\n```\n")

	code, stdout, stderr := exec("-cell", "1", path)
	if code != exitOK {
		t.Fatalf("exit = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, `cell 1 "Second"`) {
		t.Errorf("stdout = %q", stdout)
	}

	cells := mustCells(t, read(t, path))
	if len(cells[0].Results) != 0 {
		t.Error("cell 0 should not have run")
	}
	if len(cells[1].Results) != 1 {
		t.Error("cell 1 should have run")
	}
}

func mustCells(t *testing.T, src string) []*doc.Cell {
	t.Helper()
	nb, err := doc.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return nb.Cells()
}

func TestList(t *testing.T) {
	path := write(t, front+"## A\n\n```echo\nx\n```\n\n## B\n\n```sql\ny\n```\n")
	before := read(t, path)

	code, stdout, _ := exec("-list", path)
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"0\techo\tA", "1\tsql\tB"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	// -list must not run anything.
	if read(t, path) != before {
		t.Error("-list modified the notebook")
	}
}

func TestNoRunnableCells(t *testing.T) {
	path := write(t, front+"## Only sql\n\n```sql\nSELECT 1\n```\n")
	code, stdout, _ := exec(path)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout, "no echo cells to run") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestFailedRunExitsProblem(t *testing.T) {
	// A broken engine is a failed run: nothing is persisted and the exit code says
	// so. Contrast with a domain failure, which is a successful run.
	path := write(t, front+"## Broken\n\n```echo {broken}\nx\n```\n")
	before := read(t, path)

	code, _, stderr := exec(path)
	if code != exitProblem {
		t.Fatalf("exit = %d, want %d", code, exitProblem)
	}
	if !strings.Contains(stderr, "Broken") {
		t.Errorf("stderr = %q", stderr)
	}
	if read(t, path) != before {
		t.Error("a failed run must persist nothing")
	}
}

// TestDomainFailureExitsOK is the distinction stated as an exit code: a non-zero domain
// status is a run that worked.
func TestDomainFailureExitsOK(t *testing.T) {
	path := write(t, front+"## Fails\n\n```echo {status=1}\nboom\n```\n")
	code, stdout, stderr := exec(path)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d — a domain failure is a successful run\nstderr: %s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, ": error") {
		t.Errorf("stdout = %q", stdout)
	}
	if !strings.Contains(read(t, path), "```error {status=1") {
		t.Error("an error block should have been persisted")
	}
}

func TestUsageErrors(t *testing.T) {
	path := write(t, front+"## A\n\n```echo\nx\n```\n")

	tests := []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"two notebooks", []string{path, path}},
		{"unknown flag", []string{"-nope", path}},
		{"missing file", []string{filepath.Join(t.TempDir(), "nope.md")}},
		{"not a notebook", []string{write(t, "not a notebook\n")}},
		{"cell index out of range", []string{"-cell", "99", path}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code, _, _ := exec(tt.args...); code != exitUsage {
				t.Errorf("exit = %d, want %d", code, exitUsage)
			}
		})
	}
}

func TestTruncationReported(t *testing.T) {
	// The default cap is 1 MiB, so produce more than that.
	path := write(t, front+"## Long\n\n```echo {repeat=120000}\nabcdefghij\n```\n")
	code, stdout, stderr := exec(path)
	if code != exitOK {
		t.Fatalf("exit = %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "(truncated)") {
		t.Errorf("stdout = %q, want truncation reported", stdout)
	}
	got := read(t, path)
	if !strings.Contains(got, "truncated}") {
		t.Error("truncated flag missing from the notebook")
	}
	if _, err := doc.Parse([]byte(got)); err != nil {
		t.Errorf("the truncated notebook does not parse: %v", err)
	}
}

// TestUnclosedFenceIsReportedNotWritten: the run refuses rather than corrupting the
// cell by appending inside its fence body.
func TestUnclosedFenceIsReportedNotWritten(t *testing.T) {
	path := write(t, front+"## Unclosed\n\n```echo\nhello\n")
	before := read(t, path)

	code, _, stderr := exec(path)
	if code != exitProblem {
		t.Fatalf("exit = %d, want %d", code, exitProblem)
	}
	if !strings.Contains(stderr, "unclosed source fence") {
		t.Errorf("stderr = %q", stderr)
	}
	if read(t, path) != before {
		t.Error("the notebook must be untouched")
	}
}
