package doc

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/pmuston/notekit/meta"
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

func TestSetSource(t *testing.T) {
	tests := []struct {
		name string
		body string
		text string
		want string
	}{
		{
			name: "plain replacement keeps the info string",
			body: "## H\n\n```sh {format=csv, id=aaaa2345}\nold\n```\n",
			text: "new\n",
			want: "## H\n\n```sh {format=csv, id=aaaa2345}\nnew\n```\n",
		},
		{
			// Fence-length safety applies to a source fence too: without widening,
			// the body would terminate its own fence.
			name: "widens for a three-backtick run",
			body: "## H\n\n```sh\nold\n```\n",
			text: "printf '```'\n",
			want: "## H\n\n````sh\nprintf '```'\n````\n",
		},
		{
			name: "widens for a five-backtick run",
			body: "## H\n\n```sh\nold\n```\n",
			text: "echo '`````'\n",
			want: "## H\n\n``````sh\necho '`````'\n``````\n",
		},
		{
			// A tilde fence is bounded by tilde runs, not backtick runs.
			name: "tilde fence preserved and measured on tildes",
			body: "## H\n\n~~~sh\nold\n~~~\n",
			text: "echo '~~~~'\n",
			want: "## H\n\n~~~~~sh\necho '~~~~'\n~~~~~\n",
		},
		{
			name: "backticks in a tilde fence do not widen it",
			body: "## H\n\n~~~sh\nold\n~~~\n",
			text: "echo '```'\n",
			want: "## H\n\n~~~sh\necho '```'\n~~~\n",
		},
		{
			name: "missing trailing newline is added",
			body: "## H\n\n```sh\nold\n```\n",
			text: "no newline",
			want: "## H\n\n```sh\nno newline\n```\n",
		},
		{
			name: "empty body",
			body: "## H\n\n```sh\nold\n```\n",
			text: "",
			want: "## H\n\n```sh\n```\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := mustParse(t, front+tt.body)
			edit, err := n.Cells()[0].SetSource(tt.text)
			if err != nil {
				t.Fatalf("SetSource: %v", err)
			}
			out, err := n.Apply(edit)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got, want := string(out), front+tt.want; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}

			// The result must re-parse as one cell whose body is what was asked for.
			n2 := mustParse(t, string(out))
			if len(n2.Cells()) != 1 {
				t.Fatalf("got %d cells, want 1 — the fence broke", len(n2.Cells()))
			}
			wantBody := tt.text
			if wantBody != "" && !strings.HasSuffix(wantBody, "\n") {
				wantBody += "\n"
			}
			if got := n2.Cells()[0].SourceText(); got != wantBody {
				t.Errorf("SourceText() = %q, want %q", got, wantBody)
			}
			// Results are outside the source fence, so a source edit leaves the
			// info string — and any id — exactly as it was.
			if n2.Cells()[0].Info != n.Cells()[0].Info {
				t.Errorf("info string changed: %q -> %q", n.Cells()[0].Info, n2.Cells()[0].Info)
			}
		})
	}
}

func TestSetSourceLeavesResultsAlone(t *testing.T) {
	src := front + "## H\n\n```sh\nold\n```\n\n```output {run=\"x\"}\nkept\n```\n\nprose\n"
	n := mustParse(t, src)
	edit, err := n.Cells()[0].SetSource("new\n")
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.Apply(edit)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "```output {run=\"x\"}\nkept\n```") {
		t.Errorf("the result was disturbed:\n%s", got)
	}
	if !strings.Contains(got, "\nprose\n") {
		t.Errorf("prose was disturbed:\n%s", got)
	}
	assertUntouchedOutside(t, n.Bytes(), out,
		n.Cells()[0].Source.Start, n.Cells()[0].Source.End)
}

func TestSetSourceRefusesUnclosedFence(t *testing.T) {
	// Closing it on the author's behalf would be the silent repair §10 forbids, for the
	// same reason SetResult refuses.
	n := mustParse(t, front+"## H\n\n```sh\nno close\n")
	if _, err := n.Cells()[0].SetSource("new\n"); err == nil {
		t.Fatal("SetSource = nil error, want refusal for an unclosed fence")
	}
}

func TestRunLen(t *testing.T) {
	tests := []struct {
		s    string
		ch   byte
		want int
	}{
		{"", '`', 0},
		{"none", '`', 0},
		{"`", '`', 1},
		{"a``b```c", '`', 3},
		{"```\n`````\n", '`', 5},
		{"~~~~", '~', 4},
		{"```", '~', 0},
	}
	for _, tt := range tests {
		if got := runLen(tt.s, tt.ch); got != tt.want {
			t.Errorf("runLen(%q, %q) = %d, want %d", tt.s, tt.ch, got, tt.want)
		}
	}
}

func TestAppendCell(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "into an empty notebook",
			body: "",
			want: "## New\n\n```sh\necho hi\n```\n",
		},
		{
			name: "into a prose-only notebook",
			body: "Just prose.\n",
			want: "Just prose.\n\n## New\n\n```sh\necho hi\n```\n",
		},
		{
			name: "after an existing cell",
			body: "## First\n\n```sh\na\n```\n",
			want: "## First\n\n```sh\na\n```\n\n## New\n\n```sh\necho hi\n```\n",
		},
		{
			name: "after a cell with a result",
			body: "## First\n\n```sh\na\n```\n\n```output\nr\n```\n",
			want: "## First\n\n```sh\na\n```\n\n```output\nr\n```\n\n## New\n\n```sh\necho hi\n```\n",
		},
		{
			// No trailing newline: the insertion has to end the line first.
			name: "when the file does not end with a newline",
			body: "## First\n\n```sh\na\n```",
			want: "## First\n\n```sh\na\n```\n\n## New\n\n```sh\necho hi\n```\n",
		},
		{
			name: "when the file already ends with a blank line",
			body: "## First\n\n```sh\na\n```\n\n",
			want: "## First\n\n```sh\na\n```\n\n## New\n\n```sh\necho hi\n```\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := mustParse(t, front+tt.body)
			before := len(n.Cells())

			edit, err := n.AppendCell(NewCell{Heading: "New", Lang: "sh", Body: "echo hi\n"})
			if err != nil {
				t.Fatalf("AppendCell: %v", err)
			}
			out, err := n.Apply(edit)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got, want := string(out), front+tt.want; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}

			n2 := mustParse(t, string(out))
			if len(n2.Cells()) != before+1 {
				t.Fatalf("cell count %d -> %d, want +1", before, len(n2.Cells()))
			}
			last := n2.Cells()[len(n2.Cells())-1]
			if last.HeadingText != "New" || last.Lang != "sh" || last.SourceText() != "echo hi\n" {
				t.Errorf("new cell = %#v", last)
			}
			if !last.Closed {
				t.Error("the new cell's fence is not closed")
			}
		})
	}
}

func TestInsertCellAroundAnExistingCell(t *testing.T) {
	body := "## First\n\n```sh\na\n```\n\n## Second\n\n```sh\nb\n```\n"

	t.Run("after the first", func(t *testing.T) {
		n := mustParse(t, front+body)
		edit, err := n.InsertCellAfter(n.Cells()[0], NewCell{Heading: "Middle", Lang: "sh", Body: "m\n"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := n.Apply(edit)
		if err != nil {
			t.Fatal(err)
		}
		want := front + "## First\n\n```sh\na\n```\n\n## Middle\n\n```sh\nm\n```\n\n## Second\n\n```sh\nb\n```\n"
		if string(out) != want {
			t.Fatalf("got %q\nwant %q", out, want)
		}
		cells := mustParse(t, string(out)).Cells()
		headings := []string{cells[0].HeadingText, cells[1].HeadingText, cells[2].HeadingText}
		if headings[0] != "First" || headings[1] != "Middle" || headings[2] != "Second" {
			t.Errorf("order = %v", headings)
		}
	})

	t.Run("before the first", func(t *testing.T) {
		n := mustParse(t, front+body)
		edit, err := n.InsertCellBefore(n.Cells()[0], NewCell{Heading: "Zeroth", Lang: "sh", Body: "z\n"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := n.Apply(edit)
		if err != nil {
			t.Fatal(err)
		}
		cells := mustParse(t, string(out)).Cells()
		if len(cells) != 3 || cells[0].HeadingText != "Zeroth" {
			t.Errorf("first cell = %q, want Zeroth (%d cells)", cells[0].HeadingText, len(cells))
		}
		// The preamble must not have been swallowed into the new section.
		if !strings.Contains(string(out), front+"## Zeroth\n") {
			t.Errorf("unexpected layout:\n%s", out)
		}
	})

	t.Run("before the second", func(t *testing.T) {
		n := mustParse(t, front+body)
		edit, err := n.InsertCellBefore(n.Cells()[1], NewCell{Heading: "Middle", Lang: "sh", Body: "m\n"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := n.Apply(edit)
		if err != nil {
			t.Fatal(err)
		}
		cells := mustParse(t, string(out)).Cells()
		if len(cells) != 3 || cells[1].HeadingText != "Middle" {
			t.Errorf("cells = %v", cells)
		}
	})
}

func TestInsertCellPreservesEverythingElse(t *testing.T) {
	body := "Opening.\n\n## First\n\n prose\n\n```sh\na\n```\n\n```output {run=\"x\"}\nr\n```\n\ntrailing\n"
	n := mustParse(t, front+body)

	edit, err := n.AppendCell(NewCell{Heading: "New", Lang: "sh", Body: "n\n"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.Apply(edit)
	if err != nil {
		t.Fatal(err)
	}
	// Everything that was there is byte-identical; only an insertion happened.
	if !strings.HasPrefix(string(out), front+body) {
		t.Errorf("existing content changed:\n%s", out)
	}
	n2 := mustParse(t, string(out))
	if got := string(n2.Cells()[0].Results[0].Span.In(n2.Bytes())); !strings.Contains(got, `run="x"`) {
		t.Errorf("the existing result changed: %q", got)
	}
}

func TestNewCellMetadataAndLevels(t *testing.T) {
	n := mustParse(t, front+"## First\n\n```sh\na\n```\n")

	edit, err := n.AppendCell(NewCell{
		Heading: "Table", Level: 3, Lang: "sh",
		Meta: []meta.Entry{{Key: "format", Value: "csv"}},
		Body: "printf 'a,b\\n'\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.Apply(edit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "### Table\n\n```sh {format=csv}\n") {
		t.Errorf("level or metadata wrong:\n%s", out)
	}
	c := mustParse(t, string(out)).Cells()[1]
	if c.Level != 3 {
		t.Errorf("Level = %d, want 3", c.Level)
	}
	if e, ok := c.Meta.Get("format"); !ok || e.Value != "csv" {
		t.Errorf("format = %#v", e)
	}
}

func TestNewCellFenceWidening(t *testing.T) {
	// Fence-length safety applies to a new cell's fence too.
	n := mustParse(t, front+"")
	edit, err := n.AppendCell(NewCell{Heading: "H", Lang: "sh", Body: "echo '```'\n"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.Apply(edit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "````sh\n") {
		t.Errorf("fence not widened:\n%s", out)
	}
	cells := mustParse(t, string(out)).Cells()
	if len(cells) != 1 || cells[0].SourceText() != "echo '```'\n" {
		t.Errorf("cells = %#v", cells)
	}
}

func TestNewCellEmptyHeadingAndBody(t *testing.T) {
	n := mustParse(t, front+"")
	edit, err := n.AppendCell(NewCell{Lang: "sh"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.Apply(edit)
	if err != nil {
		t.Fatal(err)
	}
	// `##` alone is a valid ATX heading, and an empty slug is permitted (§5.2).
	if got, want := string(out), front+"##\n\n```sh\n```\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	cells := mustParse(t, string(out)).Cells()
	if len(cells) != 1 {
		t.Fatalf("got %d cells, want 1", len(cells))
	}
	if cells[0].Slug != "" || cells[0].HeadingText != "" {
		t.Errorf("cell = %#v", cells[0])
	}
}

func TestNewCellValidation(t *testing.T) {
	n := mustParse(t, front+"## A\n\n```sh\na\n```\n")
	tests := []struct {
		name string
		spec NewCell
	}{
		{"no language tag", NewCell{Heading: "H"}},
		// §4.3: a section whose first fence is tagged output or error holds no cell,
		// so creating one would produce a cell that is not a cell.
		{"output tag", NewCell{Heading: "H", Lang: "output"}},
		{"error tag", NewCell{Heading: "H", Lang: "error"}},
		{"level 1", NewCell{Heading: "H", Level: 1, Lang: "sh"}},
		{"level 7", NewCell{Heading: "H", Level: 7, Lang: "sh"}},
		{"newline in heading", NewCell{Heading: "one\ntwo", Lang: "sh"}},
		{"tag with a space", NewCell{Heading: "H", Lang: "s h"}},
		{"invalid metadata key", NewCell{Heading: "H", Lang: "sh", Meta: []meta.Entry{{Key: "Bad", Value: "1"}}}},
		{"duplicate metadata keys", NewCell{Heading: "H", Lang: "sh",
			Meta: []meta.Entry{{Key: "a", Value: "1"}, {Key: "a", Value: "2"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := n.AppendCell(tt.spec); err == nil {
				t.Error("want error")
			}
		})
	}
	if _, err := n.InsertCellAfter(nil, NewCell{Heading: "H", Lang: "sh"}); err == nil {
		t.Error("InsertCellAfter(nil) = nil error, want error")
	}
	if _, err := n.InsertCellBefore(nil, NewCell{Heading: "H", Lang: "sh"}); err == nil {
		t.Error("InsertCellBefore(nil) = nil error, want error")
	}
}

func TestDeleteCell(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		index int
		want  string
	}{
		{
			name:  "the only cell",
			body:  "Opening.\n\n## Only\n\n```sh\na\n```\n",
			index: 0,
			want:  "Opening.\n\n",
		},
		{
			name:  "the first of two",
			body:  "## First\n\n```sh\na\n```\n\n## Second\n\n```sh\nb\n```\n",
			index: 0,
			want:  "## Second\n\n```sh\nb\n```\n",
		},
		{
			name:  "the last of two",
			body:  "## First\n\n```sh\na\n```\n\n## Second\n\n```sh\nb\n```\n",
			index: 1,
			want:  "## First\n\n```sh\na\n```\n\n",
		},
		{
			name:  "the middle of three",
			body:  "## A\n\n```sh\na\n```\n\n## B\n\n```sh\nb\n```\n\n## C\n\n```sh\nc\n```\n",
			index: 1,
			want:  "## A\n\n```sh\na\n```\n\n## C\n\n```sh\nc\n```\n",
		},
		{
			// The whole section goes: heading, prose, fence, result, trailing prose.
			name:  "a cell with prose and a result",
			body:  "## A\n\nbefore\n\n```sh\na\n```\n\n```output\nr\n```\n\nafter\n\n## B\n\n```sh\nb\n```\n",
			index: 0,
			want:  "## B\n\n```sh\nb\n```\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := mustParse(t, front+tt.body)
			before := len(n.Cells())

			edit, err := n.DeleteCell(n.Cells()[tt.index])
			if err != nil {
				t.Fatalf("DeleteCell: %v", err)
			}
			out, err := n.Apply(edit)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got, want := string(out), front+tt.want; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
			n2 := mustParse(t, string(out))
			if len(n2.Cells()) != before-1 {
				t.Errorf("cell count %d -> %d, want -1", before, len(n2.Cells()))
			}
		})
	}
}

func TestDeleteCellNil(t *testing.T) {
	n := mustParse(t, front+"## A\n\n```sh\na\n```\n")
	if _, err := n.DeleteCell(nil); err == nil {
		t.Error("DeleteCell(nil) = nil error, want error")
	}
}

// TestDeleteTouchesOnlyTheSection is the invariant that matters: delete removes exactly
// the section span and not one byte more.
func TestDeleteTouchesOnlyTheSection(t *testing.T) {
	src := front + "Opening.\n\n## A\n\n```sh\na\n```\n\n## B\n\n```sh\nb\n```\n\n## C\n\n```sh\nc\n```\n"
	n := mustParse(t, src)
	target := n.Cells()[1]

	edit, err := n.DeleteCell(target)
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.Apply(edit)
	if err != nil {
		t.Fatal(err)
	}
	assertUntouchedOutside(t, n.Bytes(), out, target.Section.Start, target.Section.End)
}

// TestAppendThenDeleteRestoresEveryCell records what append-then-delete actually
// guarantees, which is not byte identity.
//
// Append seats the new heading with a blank line above it, and that blank line becomes
// part of the *previous* section — so deleting the new cell correctly leaves it alone,
// and the file ends one newline longer than it started. Absorbing it would mean a delete
// reaching outside the section it was asked to remove, which §10 does not permit and
// which would be deleting whitespace the user may have put there. Accumulating a blank
// line is the more conservative outcome, and round-trip identity is untouched either way.
func TestAppendThenDeleteRestoresEveryCell(t *testing.T) {
	src := front + "Opening.\n\n## First\n\n```sh\na\n```\n\n```output\nr\n```\n\ntrailing\n"
	n := mustParse(t, src)

	edit, err := n.AppendCell(NewCell{Heading: "Temp", Lang: "sh", Body: "t\n"})
	if err != nil {
		t.Fatal(err)
	}
	withCell, err := n.Apply(edit)
	if err != nil {
		t.Fatal(err)
	}

	n2 := mustParse(t, string(withCell))
	cells := n2.Cells()
	del, err := n2.DeleteCell(cells[len(cells)-1])
	if err != nil {
		t.Fatal(err)
	}
	back, err := n2.Apply(del)
	if err != nil {
		t.Fatal(err)
	}

	if strings.TrimRight(string(back), "\n") != strings.TrimRight(src, "\n") {
		t.Errorf("content differs beyond trailing whitespace:\n got %q\nwant %q", back, src)
	}
	// Every original cell survives byte-for-byte.
	n3 := mustParse(t, string(back))
	orig := mustParse(t, src)
	if len(n3.Cells()) != len(orig.Cells()) {
		t.Fatalf("cell count %d, want %d", len(n3.Cells()), len(orig.Cells()))
	}
	for i, c := range orig.Cells() {
		got := n3.Cells()[i]
		if got.HeadingText != c.HeadingText || got.SourceText() != c.SourceText() {
			t.Errorf("cell %d changed: %q/%q -> %q/%q",
				i, c.HeadingText, c.SourceText(), got.HeadingText, got.SourceText())
		}
	}
	// And it still round-trips, which is the normative property (§10).
	if string(n3.Bytes()) != string(back) {
		t.Error("the result does not round-trip")
	}
}

func TestProseAfterWhenResultsRunToTheSectionEnd(t *testing.T) {
	// The clamp exists for a section whose result position ends exactly where the
	// section does, leaving nothing after it to edit.
	n := mustParse(t, front+"## H\n\n```sh\na\n```\n\n```output\nr\n```\n")
	c := n.Cells()[0]
	got := c.ProseAfter()
	if !got.Empty() {
		t.Errorf("ProseAfter = %v (%q), want empty", got, string(got.In(n.Bytes())))
	}
	if got.Start < 0 || got.End > len(n.Bytes()) {
		t.Errorf("ProseAfter = %v is out of bounds", got)
	}
}

func TestInsertCellClampsPosition(t *testing.T) {
	// A boundary before the body start clamps forward, so an insertion can never land
	// inside the front matter.
	n := mustParse(t, front+"## A\n\n```sh\na\n```\n")
	edit, err := n.insertCellAt(NewCell{Heading: "H", Lang: "sh", Body: "x\n"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if edit.Span.Start < n.Body().Start {
		t.Errorf("insertion at %d is inside the front matter (body starts at %d)",
			edit.Span.Start, n.Body().Start)
	}
	out, err := n.Apply(edit)
	if err != nil {
		t.Fatal(err)
	}
	n2 := mustParse(t, string(out))
	if n2.Version() != Version {
		t.Error("the front matter was damaged")
	}
	if len(n2.Cells()) != 2 {
		t.Errorf("got %d cells, want 2:\n%s", len(n2.Cells()), out)
	}

	// And a position past the end clamps back.
	edit, err = n.insertCellAt(NewCell{Heading: "Z", Lang: "sh", Body: "z\n"}, len(n.Bytes())+500)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Apply(edit); err != nil {
		t.Errorf("Apply after clamping: %v", err)
	}
}

func TestSetSourceOnATildeFenceWithNoRecordedChar(t *testing.T) {
	// A cell built by hand rather than parsed has no fence character recorded, and the
	// default must still produce a valid fence.
	n := mustParse(t, front+"## H\n\n```sh\na\n```\n")
	c := n.Cells()[0]
	c.fenceCh = 0
	edit, err := c.SetSource("new\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(edit.Text, "```sh") {
		t.Errorf("edit = %q, want a backtick fence by default", edit.Text)
	}
}
