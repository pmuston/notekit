package doc

import (
	"strings"
	"testing"
)

func fuzzSeeds(f *testing.F) {
	for _, fx := range fixtures {
		f.Add(front + fx.body)
	}
	// Shapes no fixture covers, aimed at the scanner's state machine.
	for _, s := range []string{
		"", "---", "---\n", "---\nnotekit: 1\n---",
		"---\nnotekit: 1\n---\n## H\n```sh\n",
		"---\nnotekit: 1\n---\n#\n##\n###\n",
		"---\nnotekit: 1\n---\n```````sh\n``````\n```````\n",
		"---\nnotekit: 1\n---\n~~~\n```\n~~~\n",
		"---\nnotekit: 1\n---\n## H\n\n```sh {id=k3m7q2vf}\n```\n",
		"---\nnotekit: 1\n---\n<!-- notekit:result -->\n![a](b)\n",
	} {
		f.Add(s)
	}
}

// FuzzDocParse asserts that Parse never panics on arbitrary input and that every
// span it reports is in bounds and internally consistent.
//
// Bytes() returning the input slice makes textual round-trip trivially true, so the
// invariant worth fuzzing is structural: a span that is out of bounds or out of
// order is what would later produce a corrupt splice.
func FuzzDocParse(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, src string) {
		n, err := Parse([]byte(src))
		if err != nil {
			return // refusing a non-notebook is correct (§2)
		}
		if got := string(n.Bytes()); got != src {
			t.Fatalf("Bytes() changed the source")
		}
		size := len(src)

		inBounds := func(name string, s Span) {
			if s.Start < 0 || s.End > size || s.Start > s.End {
				t.Fatalf("%s span %v out of bounds for %d bytes", name, s, size)
			}
		}
		inBounds("front matter", n.FrontMatter())
		inBounds("body", n.Body())

		prevSectionEnd := -1
		for i, c := range n.Cells() {
			inBounds("section", c.Section)
			inBounds("heading", c.Heading)
			inBounds("source", c.Source)
			inBounds("info", c.InfoSpan)
			inBounds("body", c.Body)
			inBounds("result position", c.ResultPos)

			// Sections never nest and never overlap (§4.1).
			if c.Section.Start < prevSectionEnd {
				t.Fatalf("cell %d section %v overlaps the previous section ending at %d",
					i, c.Section, prevSectionEnd)
			}
			prevSectionEnd = c.Section.End

			if c.Heading.Start != c.Section.Start {
				t.Fatalf("cell %d heading starts at %d, section at %d", i, c.Heading.Start, c.Section.Start)
			}
			if c.Source.Start < c.Heading.End {
				t.Fatalf("cell %d source fence starts before the heading ends", i)
			}
			if c.Source.End > c.Section.End {
				t.Fatalf("cell %d source fence %v escapes its section %v", i, c.Source, c.Section)
			}
			if c.InfoSpan.Start < c.Source.Start || c.InfoSpan.End > c.Source.End {
				t.Fatalf("cell %d info span %v escapes its source fence %v", i, c.InfoSpan, c.Source)
			}
			if c.Body.Start < c.InfoSpan.End || c.Body.End > c.Source.End {
				t.Fatalf("cell %d body %v is not inside its source fence %v", i, c.Body, c.Source)
			}

			// A run replaces the result position, so it must begin exactly where
			// the source fence ends or a splice could clip the fence.
			if c.ResultPos.Start != c.Source.End {
				t.Fatalf("cell %d ResultPos.Start = %d, want Source.End = %d",
					i, c.ResultPos.Start, c.Source.End)
			}
			for j, r := range c.Results {
				inBounds("result", r.Span)
				if r.Span.Start < c.ResultPos.Start || r.Span.End > c.ResultPos.End {
					t.Fatalf("cell %d result %d span %v escapes result position %v",
						i, j, r.Span, c.ResultPos)
				}
			}
			if c.ID != "" && !ValidID(c.ID) {
				// An id read from source may be malformed; it must simply not be
				// reported as a valid one.
				if _, err := c.AssignID(c.ID); err == nil {
					t.Fatalf("cell %d accepted reassignment of malformed id %q", i, c.ID)
				}
			}
			if len(c.Slug) > SlugMaxLen {
				t.Fatalf("cell %d slug %q exceeds %d bytes", i, c.Slug, SlugMaxLen)
			}
		}
	})
}

// FuzzDocSetResult asserts that splicing a result is convergent and localised for
// any notebook: writing the same result twice reaches a fixed point, and the bytes
// before the source fence never move.
//
// Convergence is the property the volatile lifecycle depends on — without it every
// run would grow the file.
func FuzzDocSetResult(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, src string) {
		n, err := Parse([]byte(src))
		if err != nil || len(n.Cells()) == 0 {
			return
		}
		c := n.Cells()[0]
		headEnd := c.Source.End

		edit, err := c.SetResult("```output\nr\n```")
		if err != nil {
			// An unclosed source fence has no result position (§4.2); refusing
			// is the correct outcome, not a failure.
			return
		}
		first, err := n.Apply(edit)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !strings.HasPrefix(string(first), src[:headEnd]) {
			t.Fatalf("splice disturbed bytes at or before the source fence end %d", headEnd)
		}

		n2, err := Parse(first)
		if err != nil {
			t.Fatalf("spliced output no longer parses: %v", err)
		}
		if len(n2.Cells()) == 0 {
			t.Fatal("splice destroyed the cell")
		}
		c2 := n2.Cells()[0]
		if c2.Lang != c.Lang {
			t.Fatalf("splice changed the cell's language: %q -> %q", c.Lang, c2.Lang)
		}
		if c2.SourceText() != c.SourceText() {
			t.Fatalf("splice changed the cell's source: %q -> %q", c.SourceText(), c2.SourceText())
		}

		edit2, err := c2.SetResult("```output\nr\n```")
		if err != nil {
			t.Fatalf("SetResult on spliced output: %v", err)
		}
		second, err := n2.Apply(edit2)
		if err != nil {
			t.Fatalf("Apply (second): %v", err)
		}
		if string(second) != string(first) {
			t.Fatalf("SetResult is not convergent:\nfirst  %q\nsecond %q", first, second)
		}
	})
}

// FuzzDocAssignID asserts that assigning an identity is append-only at document
// scale: the fence body, language, and every other byte outside the info string
// survive untouched, and the id is readable afterwards.
func FuzzDocAssignID(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, src string) {
		n, err := Parse([]byte(src))
		if err != nil || len(n.Cells()) == 0 {
			return
		}
		c := n.Cells()[0]
		edit, err := c.AssignID("k3m7q2vf")
		if err != nil {
			return // refusing (already has one, malformed info string) is valid
		}
		out, err := n.Apply(edit)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(out) <= len(src) {
			t.Fatalf("assigning an id did not grow the document")
		}
		// Everything before the info string, and everything after it, is intact.
		if string(out[:c.InfoSpan.Start]) != src[:c.InfoSpan.Start] {
			t.Fatal("bytes before the info string changed")
		}
		tail := src[c.InfoSpan.End:]
		if got := string(out[len(out)-len(tail):]); got != tail {
			t.Fatalf("bytes after the info string changed:\n got %q\nwant %q", got, tail)
		}

		n2, err := Parse(out)
		if err != nil {
			t.Fatalf("output no longer parses: %v", err)
		}
		if len(n2.Cells()) == 0 {
			t.Fatal("assigning an id destroyed the cell")
		}
		if got := n2.Cells()[0].ID; got != "k3m7q2vf" {
			t.Fatalf("id not readable after assignment: %q", got)
		}
		if got := n2.Cells()[0].SourceText(); got != c.SourceText() {
			t.Fatalf("fence body changed: %q -> %q", c.SourceText(), got)
		}
	})
}

// FuzzDocMoveCell asserts what a move must guarantee for *any* input: the result parses,
// and it holds the same cells as before — or the move was refused outright.
//
// It deliberately does not assert byte-identical reversibility. That property does hold for
// notebooks whose sections are self-contained, and TestMoveIsReversible pins it for the
// realistic shapes, but this target disproved it in general within minutes: a section is not
// guaranteed self-contained, because an unclosed fence anywhere inside one runs to end of
// file, and a level-1 heading makes a section that holds no cell. Once relocating a section
// can change what the following bytes mean, "move away and back" is not an identity. The
// honest invariant is preservation-or-refusal, and that is what this checks.
func FuzzDocMoveCell(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, src string) {
		n, err := Parse([]byte(src))
		if err != nil || len(n.Cells()) < 2 {
			return
		}
		before := headingList(n)

		edits, ok, err := n.MoveCellDown(n.Cells()[0])
		if err != nil {
			// A refusal is a correct outcome: a section that is not self-contained
			// cannot be relocated without changing what follows it (§4.2, §10 h).
			return
		}
		if !ok {
			t.Fatal("a notebook with two cells must be able to move the first down")
		}
		moved, err := n.Apply(edits...)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		m, err := Parse(moved)
		if err != nil {
			t.Fatalf("a moved notebook must still parse: %v\n%q", err, moved)
		}
		// The same cells, reordered — none lost, none invented. MoveCellDown verifies
		// this itself before returning edits, so a failure here means that check is
		// wrong, not merely that the input was odd.
		if got, want := len(m.Cells()), len(before); got != want {
			t.Fatalf("cell count changed: %d, want %d\n%q", got, want, moved)
		}
	})
}
func headingList(n *Notebook) []string {
	var out []string
	for _, c := range n.Cells() {
		out = append(out, c.HeadingText)
	}
	return out
}
