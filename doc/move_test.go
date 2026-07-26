package doc

import (
	"bytes"
	"strings"
	"testing"
)

// moveOnce applies one move and returns the new bytes, or reports the no-op.
func moveOnce(t *testing.T, src []byte, index int, up bool) (out []byte, moved bool) {
	t.Helper()
	nb, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cells := nb.Cells()
	if index >= len(cells) {
		t.Fatalf("index %d out of range (%d cells)", index, len(cells))
	}
	var edits []Edit
	var ok bool
	if up {
		edits, ok, err = nb.MoveCellUp(cells[index])
	} else {
		edits, ok, err = nb.MoveCellDown(cells[index])
	}
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if !ok {
		if len(edits) != 0 {
			t.Errorf("a no-op move returned %d edits; it must return none so a tool "+
				"cannot rewrite the file for nothing", len(edits))
		}
		return src, false
	}
	out, err = nb.Apply(edits...)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return out, true
}

func headings(t *testing.T, src []byte) []string {
	t.Helper()
	nb, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, src)
	}
	var out []string
	for _, c := range nb.Cells() {
		out = append(out, c.HeadingText)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const threeCells = "---\nnotekit: 1\n---\n\n" +
	"## alpha\n\n```sh\necho a\n```\n\n" +
	"## beta\n\n```sh\necho b\n```\n\n" +
	"## gamma\n\n```sh\necho c\n```\n"

func TestMoveReordersCells(t *testing.T) {
	src := []byte(threeCells)

	out, moved := moveOnce(t, src, 1, true)
	if !moved {
		t.Fatal("want a move")
	}
	if got := headings(t, out); !eq(got, []string{"beta", "alpha", "gamma"}) {
		t.Errorf("up: got %v", got)
	}

	out, moved = moveOnce(t, src, 1, false)
	if !moved {
		t.Fatal("want a move")
	}
	if got := headings(t, out); !eq(got, []string{"alpha", "gamma", "beta"}) {
		t.Errorf("down: got %v", got)
	}
}

// TestMoveIsReversible is the property the swap formulation buys, and it is stronger than
// §10.1 promises: because a slot keeps its own separator and only the contents move, a cell
// moved away and back leaves the file byte-identical.
func TestMoveIsReversible(t *testing.T) {
	for _, src := range []string{
		threeCells,
		// Irregular separators: two blank lines in one seam, none in another.
		"---\nnotekit: 1\n---\n\n## a\n\n```sh\nx\n```\n\n\n## b\n\n```sh\ny\n```\n## c\n\n```sh\nz\n```\n",
		// A result block and prose travelling with their cell.
		"---\nnotekit: 1\n---\n\nintro\n\n## a\n\nsome prose\n\n```sh\nx\n```\n\n```output\nout\n```\n\n## b\n\n```sh\ny\n```\n",
		// No trailing newline at end of file.
		"---\nnotekit: 1\n---\n\n## a\n\n```sh\nx\n```\n\n## b\n\n```sh\ny\n```",
	} {
		down, moved := moveOnce(t, []byte(src), 0, false)
		if !moved {
			t.Fatal("want a move")
		}
		back, moved := moveOnce(t, down, 1, true)
		if !moved {
			t.Fatal("want a move back")
		}
		if !bytes.Equal(back, []byte(src)) {
			t.Errorf("move down then up is not byte-identical\n got %q\nwant %q", back, src)
		}
	}
}

// TestMoveNoOpWritesNothing pins §10 h: the file is the artifact, and a no-op that rewrote
// it would produce a spurious change for a reader, a diff and any watcher.
func TestMoveNoOpWritesNothing(t *testing.T) {
	src := []byte(threeCells)
	if _, moved := moveOnce(t, src, 0, true); moved {
		t.Error("the first cell cannot move up")
	}
	if _, moved := moveOnce(t, src, 2, false); moved {
		t.Error("the last cell cannot move down")
	}

	one := []byte("---\nnotekit: 1\n---\n\n## only\n\n```sh\nx\n```\n")
	if _, moved := moveOnce(t, one, 0, true); moved {
		t.Error("a one-cell notebook has nothing to move")
	}
	if _, moved := moveOnce(t, one, 0, false); moved {
		t.Error("a one-cell notebook has nothing to move")
	}
}

// TestMovePreservesBytesAndPreamble covers the two things §10 h calls out: the section's
// bytes move verbatim, and the preamble — prose belonging to no cell — stays put rather
// than being carried along or landed above.
func TestMovePreservesBytesAndPreamble(t *testing.T) {
	src := []byte("---\nnotekit: 1\n---\n\n" +
		"Introductory prose that belongs to no cell.\n\n" +
		"## alpha\n\nprose for alpha\n\n```sh {keep=\"this\", id=k3m7q2vf}\necho a\n```\n\n" +
		"```output {run=\"2026-07-26T00:00:00Z\"}\na\n```\n\n" +
		"## beta\n\n```sh\necho b\n```\n")

	out, moved := moveOnce(t, src, 1, true)
	if !moved {
		t.Fatal("want a move")
	}

	// The preamble is still the first thing after the front matter.
	body := string(out[bytes.Index(out, []byte("---\n\n"))+len("---\n\n"):])
	if !strings.HasPrefix(body, "Introductory prose") {
		t.Errorf("the preamble moved; body starts:\n%s", body[:min(120, len(body))])
	}

	// alpha's section came through verbatim: hand-authored metadata not reordered or
	// requoted, id not reassigned, prose and result still attached.
	for _, want := range []string{
		"```sh {keep=\"this\", id=k3m7q2vf}\necho a\n```",
		"prose for alpha",
		"```output {run=\"2026-07-26T00:00:00Z\"}\na\n```",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("moved section was not verbatim; missing %q\n%s", want, out)
		}
	}

	// And nothing was duplicated or lost.
	if got, want := len(out), len(src); got != want {
		t.Errorf("length changed: %d, want %d\n%s", got, want, out)
	}
	if got := headings(t, out); !eq(got, []string{"beta", "alpha"}) {
		t.Errorf("got %v", got)
	}
}

// TestMoveKeepsIDsAndSkipsCellLessSections covers the two structural cases: identity is
// non-positional (§5.1), and a heading whose section holds no cell (§4.3) is not a cell and
// stays where it is.
func TestMoveKeepsIDsAndSkipsCellLessSections(t *testing.T) {
	src := []byte("---\nnotekit: 1\n---\n\n" +
		"## alpha\n\n```sh {id=aaaaaaaa}\necho a\n```\n\n" +
		"## not a cell\n\nIts first fence is untagged, so this section has no cell.\n\n```\nplain\n```\n\n" +
		"## beta\n\n```sh {id=bbbbbbbb}\necho b\n```\n")

	nb, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(nb.Cells()) != 2 {
		t.Fatalf("want 2 cells, got %d", len(nb.Cells()))
	}

	out, moved := moveOnce(t, src, 1, true)
	if !moved {
		t.Fatal("want a move")
	}
	if got := headings(t, out); !eq(got, []string{"beta", "alpha"}) {
		t.Errorf("got %v", got)
	}

	// Identity is unchanged, so every sidecar attachment survives — §11.9's requirement.
	nb2, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, c := range nb2.Cells() {
		ids[c.HeadingText] = c.ID
	}
	if ids["alpha"] != "aaaaaaaa" || ids["beta"] != "bbbbbbbb" {
		t.Errorf("ids changed: %v", ids)
	}

	// The cell-less section stayed between them rather than being swapped or dragged.
	s := string(out)
	iBeta, iNot, iAlpha := strings.Index(s, "## beta"), strings.Index(s, "## not a cell"), strings.Index(s, "## alpha")
	if !(iBeta < iNot && iNot < iAlpha) {
		t.Errorf("cell-less section did not stay put: beta=%d notacell=%d alpha=%d", iBeta, iNot, iAlpha)
	}
}

func TestMoveRejectsAForeignCell(t *testing.T) {
	a, err := Parse([]byte(threeCells))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse([]byte(threeCells))
	if err != nil {
		t.Fatal(err)
	}
	// A cell carries spans into one particular parse; splicing it against another is how a
	// stale offset writes to the wrong place while still being in range.
	if _, _, err := a.MoveCellUp(b.Cells()[1]); err == nil {
		t.Error("want an error for a cell from another parse")
	}
	if _, _, err := a.MoveCellUp(nil); err == nil {
		t.Error("want an error for a nil cell")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestMoveRefusesASectionThatIsNotSelfContained covers what a fuzzer found within minutes of
// this operation existing. An unclosed fence runs to end of file (§4.2), so it is harmless
// only while its cell is last; relocating it turns every following heading into its body,
// and moving another cell *past* it does the same, since a swap relocates both.
//
// Closing the fence first would be the silent repair §10 forbids, so this refuses.
func TestMoveRefusesASectionThatIsNotSelfContained(t *testing.T) {
	t.Run("unclosed source fence", func(t *testing.T) {
		// The second cell's fence is never closed.
		src := []byte("---\nnotekit: 1\n---\n\n## a\n\n```sh\nx\n```\n\n## b\n\n```sh\ny\n")
		nb, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if len(nb.Cells()) != 2 {
			t.Fatalf("want 2 cells, got %d", len(nb.Cells()))
		}
		for _, tc := range []struct {
			name string
			run  func() ([]Edit, bool, error)
		}{
			{"moving it up", func() ([]Edit, bool, error) { return nb.MoveCellUp(nb.Cells()[1]) }},
			{"moving the other down past it", func() ([]Edit, bool, error) { return nb.MoveCellDown(nb.Cells()[0]) }},
		} {
			edits, ok, err := tc.run()
			if err == nil {
				t.Errorf("%s: want a refusal", tc.name)
			}
			if ok || len(edits) != 0 {
				t.Errorf("%s: a refusal must yield no edits (ok=%v, %d edits)", tc.name, ok, len(edits))
			}
		}
	})

	// The backstop — [Notebook.trySwap] verifying its own edits — cannot be reached by a
	// hand-written case: an unclosed fence swallows the rest of the file, so a notebook
	// containing one has no *second* cell to move. It is exercised instead by the inputs
	// FuzzDocMoveCell discovered, which live in testdata/fuzz and are replayed on every
	// plain `go test` run. Two of them collapsed two cells into one before the check
	// existed.
}
