package doc

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func asError(err error, target any) bool { return errors.As(err, target) }

// assertUntouchedOutside checks that a splice confined to [headEnd, tailStart)
// changed nothing outside it: every byte up to headEnd is identical, and every byte
// from tailStart to end of file is identical.
//
// This is the precise form of §11.5's "only the expected byte range changed". It
// holds for insertions as well as replacements, which a diff-span bound does not.
func assertUntouchedOutside(t *testing.T, before, after []byte, headEnd, tailStart int) {
	t.Helper()
	if !bytes.Equal(before[:headEnd], after[:headEnd]) {
		t.Errorf("bytes before offset %d changed:\n got %q\nwant %q",
			headEnd, after[:min(headEnd, len(after))], before[:headEnd])
	}
	tail := before[tailStart:]
	if len(after) < len(tail) {
		t.Fatalf("output shorter than the untouched tail (%d < %d)", len(after), len(tail))
	}
	if got := after[len(after)-len(tail):]; !bytes.Equal(tail, got) {
		t.Errorf("bytes after offset %d changed:\n got %q\nwant %q", tailStart, got, tail)
	}
}

// TestSetResultTouchesOnlyResultPosition is §11.5 stated precisely: run a cell and
// verify that exactly the expected byte range changed — not merely that the output
// looks right.
func TestSetResultTouchesOnlyResultPosition(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string // expected document after the splice
	}{
		{
			name: "into empty result position",
			body: "## H\n\n```sh\na\n```\n\nprose after\n",
			want: "## H\n\n```sh\na\n```\n\n```output\nnew\n```\n\nprose after\n",
		},
		{
			name: "replacing one output",
			body: "## H\n\n```sh\na\n```\n\n```output\nold\n```\n\nprose after\n",
			want: "## H\n\n```sh\na\n```\n\n```output\nnew\n```\n\nprose after\n",
		},
		{
			name: "replacing two outputs with one",
			body: "## H\n\n```sh\na\n```\n\n```output\none\n```\n\n```output\ntwo\n```\n\nprose\n",
			want: "## H\n\n```sh\na\n```\n\n```output\nnew\n```\n\nprose\n",
		},
		{
			name: "replacing mixed forms with one",
			body: "## H\n\n```sh {id=bbbb2345}\na\n```\n\n```output\ntext\n```\n\n" +
				"<!-- notekit:result kind=graph -->\n![x](n.assets/h--bbbb2345.png)\n\nprose\n",
			want: "## H\n\n```sh {id=bbbb2345}\na\n```\n\n```output\nnew\n```\n\nprose\n",
		},
		{
			name: "replacing an error block",
			body: "## H\n\n```sh\na\n```\n\n```error {status=127}\nboom\n```\n",
			want: "## H\n\n```sh\na\n```\n\n```output\nnew\n```\n",
		},
		{
			name: "no trailing newline after fence",
			body: "## H\n\n```sh\na\n```",
			want: "## H\n\n```sh\na\n```\n\n```output\nnew\n```\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := front + tt.body
			n := mustParse(t, src)
			c := n.Cells()[0]

			edit, err := c.SetResult("```output\nnew\n```")
			if err != nil {
				t.Fatalf("SetResult: %v", err)
			}
			out, err := n.Apply(edit)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got, want := string(out), front+tt.want; got != want {
				t.Fatalf("document mismatch:\n got %q\nwant %q", got, want)
			}

			// Localisation, asserted as prefix and suffix identity rather than a
			// diff span: for an insertion into an empty result position the
			// changed range is genuinely ambiguous (any equivalent insertion
			// point yields the same bytes), so a diff-based bound would be
			// checking the heuristic, not the splice.
			assertUntouchedOutside(t, n.Bytes(), out, c.Source.End, c.ResultPos.End)
		})
	}
}

// TestSetResultRefusesUnclosedFence is a regression test for a bug FuzzDocSetResult
// found: an unclosed source fence runs to end of file, so appending a result there
// lands *inside the fence body* and silently corrupts the cell.
//
// Refusing is the only correct answer. Closing the fence first would be the silent
// repair §10 forbids, and writing anyway destroys the notebook. The cell stays
// readable and runnable — only persisting its result is impossible.
func TestSetResultRefusesUnclosedFence(t *testing.T) {
	src := front + "## H\n\n```sh\nno closing fence\n"
	n := mustParse(t, src)
	cells := n.Cells()
	if len(cells) != 1 {
		t.Fatalf("got %d cells, want 1", len(cells))
	}
	c := cells[0]

	if c.Closed {
		t.Error("Closed = true, want false")
	}
	// The cell is still fully readable: a tool may run it.
	if got := c.SourceText(); got != "no closing fence\n" {
		t.Errorf("SourceText() = %q", got)
	}
	if _, err := c.SetResult("```output\nr\n```"); err == nil {
		t.Fatal("SetResult = nil error, want refusal for an unclosed fence")
	}
	// Assigning an id remains safe: the info string is on the opening line.
	if _, err := c.AssignID("k3m7q2vf"); err != nil {
		t.Errorf("AssignID on an unclosed fence = %v, want success", err)
	}
}

func TestClosedFenceReported(t *testing.T) {
	n := mustParse(t, front+"## H\n\n```sh\na\n```\n")
	if !n.Cells()[0].Closed {
		t.Error("Closed = false, want true for a terminated fence")
	}
}

func TestSetResultClears(t *testing.T) {
	src := front + "## H\n\n```sh\na\n```\n\n```output\nold\n```\n\nprose\n"
	n := mustParse(t, src)
	clear, err := n.Cells()[0].SetResult("")
	if err != nil {
		t.Fatalf("SetResult: %v", err)
	}
	out, err := n.Apply(clear)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := front + "## H\n\n```sh\na\n```\n\nprose\n"
	if string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// TestSetResultIsIdempotent guards the volatile lifecycle: writing the same result
// twice must converge, or every run would grow the file.
func TestSetResultIsIdempotent(t *testing.T) {
	src := front + "## H\n\n```sh\na\n```\n\nprose\n"
	n := mustParse(t, src)
	e1, err := n.Cells()[0].SetResult("```output\nr\n```")
	if err != nil {
		t.Fatal(err)
	}
	first, err := n.Apply(e1)
	if err != nil {
		t.Fatal(err)
	}
	n2 := mustParse(t, string(first))
	e2, err := n2.Cells()[0].SetResult("```output\nr\n```")
	if err != nil {
		t.Fatal(err)
	}
	second, err := n2.Apply(e2)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) {
		t.Errorf("not idempotent:\nfirst  %q\nsecond %q", first, second)
	}
}

func TestAssignID(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "fence with no metadata",
			body: "## H\n\n```cypher\na\n```\n",
			want: "## H\n\n```cypher {id=k3m7q2vf}\na\n```\n",
		},
		{
			name: "fence with metadata",
			body: "## H\n\n```cypher {format=csv}\na\n```\n",
			want: "## H\n\n```cypher {format=csv, id=k3m7q2vf}\na\n```\n",
		},
		{
			// §9: append-only. Hand-authored spacing survives untouched.
			name: "untidy hand-authored metadata",
			body: "## H\n\n```cypher {  format = csv  }\na\n```\n",
			want: "## H\n\n```cypher {  format = csv, id=k3m7q2vf  }\na\n```\n",
		},
		{
			name: "tilde fence",
			body: "## H\n\n~~~cypher {a=1}\na\n~~~\n",
			want: "## H\n\n~~~cypher {a=1, id=k3m7q2vf}\na\n~~~\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := front + tt.body
			n := mustParse(t, src)
			c := n.Cells()[0]

			edit, err := c.AssignID("k3m7q2vf")
			if err != nil {
				t.Fatalf("AssignID: %v", err)
			}
			out, err := n.Apply(edit)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got, want := string(out), front+tt.want; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}

			// The edit must land inside the info string and nowhere else — §10
			// permits this one source-fence write and nothing wider.
			span, _ := changed(n.Bytes(), out)
			if span.Start < c.InfoSpan.Start || span.End > c.InfoSpan.End {
				t.Errorf("changed span %v escapes info span %v", span, c.InfoSpan)
			}

			// Re-parsing must see the id, and the body must be unchanged.
			n2 := mustParse(t, string(out))
			if got := n2.Cells()[0].ID; got != "k3m7q2vf" {
				t.Errorf("re-parsed ID = %q", got)
			}
			if got, want := n2.Cells()[0].SourceText(), c.SourceText(); got != want {
				t.Errorf("source body changed: %q -> %q", want, got)
			}
		})
	}
}

func TestAssignIDErrors(t *testing.T) {
	t.Run("already assigned", func(t *testing.T) {
		n := mustParse(t, front+"## H\n\n```sh {id=k3m7q2vf}\na\n```\n")
		if _, err := n.Cells()[0].AssignID("bbbb2345"); err == nil {
			t.Fatal("want error for a cell that already has an id")
		}
	})
	t.Run("invalid id", func(t *testing.T) {
		n := mustParse(t, front+"## H\n\n```sh\na\n```\n")
		for _, bad := range []string{"", "short", "toolongtoolong", "UPPERCA", "has-dash", "abcdefg1"} {
			if _, err := n.Cells()[0].AssignID(bad); err == nil {
				t.Errorf("AssignID(%q) = nil error, want error", bad)
			}
		}
	})
	t.Run("id present as a valueless flag", func(t *testing.T) {
		// `{id}` parses as a boolean flag, so Cell.ID stays empty while the key
		// is nonetheless taken. Insert must refuse rather than write a second
		// `id` entry, which would make the fence unparseable.
		n := mustParse(t, front+"## H\n\n```sh {id}\na\n```\n")
		c := n.Cells()[0]
		if c.ID != "" {
			t.Fatalf("ID = %q, want empty for a valueless id flag", c.ID)
		}
		if _, err := c.AssignID("k3m7q2vf"); err == nil {
			t.Fatal("AssignID = nil error, want refusal when the id key is already present")
		}
	})
	t.Run("malformed info string", func(t *testing.T) {
		n := mustParse(t, front+"## H\n\n```sh {a=1, a=2}\na\n```\n")
		if _, err := n.Cells()[0].AssignID("k3m7q2vf"); err == nil {
			t.Fatal("want error when the info string cannot be parsed")
		}
	})
}

// TestAssignIDAndSetResultTogether is the real sequence for a sidecar-producing
// run: one splice writing both the identity and the result.
func TestAssignIDAndSetResultTogether(t *testing.T) {
	src := front + "## Module wiring\n\n```cypher\nMATCH (m) RETURN m\n```\n\ntrailing prose\n"
	n := mustParse(t, src)
	c := n.Cells()[0]

	idEdit, err := c.AssignID("k3m7q2vf")
	if err != nil {
		t.Fatal(err)
	}
	ref := "<!-- notekit:result kind=graph, run=\"2026-07-25T10:00:00Z\" -->\n" +
		"![Module wiring](n.assets/module-wiring--k3m7q2vf.png)"
	resEdit, err := c.SetResult(ref)
	if err != nil {
		t.Fatalf("SetResult: %v", err)
	}
	out, err := n.Apply(idEdit, resEdit)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := front + "## Module wiring\n\n```cypher {id=k3m7q2vf}\nMATCH (m) RETURN m\n```\n\n" +
		ref + "\n\ntrailing prose\n"
	if string(out) != want {
		t.Fatalf("got %q\nwant %q", out, want)
	}

	n2 := mustParse(t, string(out))
	c2 := n2.Cells()[0]
	if c2.ID != "k3m7q2vf" {
		t.Errorf("ID = %q", c2.ID)
	}
	if len(c2.Results) != 1 || c2.Results[0].Form != ResultSidecar {
		t.Errorf("results = %#v, want one sidecar", c2.Results)
	}
}

func TestApplyRejectsOverlapAndOutOfBounds(t *testing.T) {
	n := mustParse(t, front+"## H\n\n```sh\na\n```\n")
	base := len(front)

	tests := []struct {
		name  string
		edits []Edit
	}{
		{"overlapping", []Edit{
			{Span: Span{base, base + 5}, Text: "x"},
			{Span: Span{base + 3, base + 8}, Text: "y"},
		}},
		{"negative start", []Edit{{Span: Span{-1, 2}, Text: "x"}}},
		{"end past source", []Edit{{Span: Span{0, len(n.Bytes()) + 1}, Text: "x"}}},
		{"inverted span", []Edit{{Span: Span{10, 4}, Text: "x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := n.Apply(tt.edits...); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestApplyOrderIndependent(t *testing.T) {
	// Edits may arrive in any order; Apply sorts them.
	src := front + "## A\n\n```sh\na\n```\n\n## B\n\n```sh\nb\n```\n"
	n := mustParse(t, src)
	a, b := n.Cells()[0], n.Cells()[1]

	ea, err := a.SetResult("```output\nra\n```")
	if err != nil {
		t.Fatal(err)
	}
	eb, err := b.SetResult("```output\nrb\n```")
	if err != nil {
		t.Fatal(err)
	}
	forward, err := n.Apply(ea, eb)
	if err != nil {
		t.Fatal(err)
	}
	backward, err := n.Apply(eb, ea)
	if err != nil {
		t.Fatal(err)
	}
	if string(forward) != string(backward) {
		t.Errorf("order changed the result:\n%q\n%q", forward, backward)
	}
	if !strings.Contains(string(forward), "ra") || !strings.Contains(string(forward), "rb") {
		t.Error("both results should be present")
	}
}

// TestEditProseLeavesCellsIntact covers the M2 prose-editing path, built now
// because it is the same machinery.
func TestEditProseLeavesCellsIntact(t *testing.T) {
	src := front + "## H\n\nOriginal prose.\n\n```sh\na\n```\n"
	n := mustParse(t, src)

	start := strings.Index(src, "Original prose.")
	span := Span{Start: start, End: start + len("Original prose.")}
	out, err := n.Apply(n.EditProse(span, "Rewritten prose."))
	if err != nil {
		t.Fatal(err)
	}
	want := front + "## H\n\nRewritten prose.\n\n```sh\na\n```\n"
	if string(out) != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	n2 := mustParse(t, string(out))
	if got := n2.Cells()[0].SourceText(); got != "a\n" {
		t.Errorf("cell body changed: %q", got)
	}
}

// TestApplyTwoInsertionsAtSamePosition covers the ordering rule for edits that share a
// start offset: two empty spans at one position do not overlap, so both apply, and the
// stable sort keeps them in the order given.
func TestApplyTwoInsertionsAtSamePosition(t *testing.T) {
	n := mustParse(t, front+"## H\n\n```sh\na\n```\n")
	at := len(n.Bytes())
	out, err := n.Apply(
		Edit{Span: Span{at, at}, Text: "first\n"},
		Edit{Span: Span{at, at}, Text: "second\n"},
	)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := front + "## H\n\n```sh\na\n```\nfirst\nsecond\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}
