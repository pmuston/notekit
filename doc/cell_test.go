package doc

import "testing"

// TestRoundTripAllFixtures is the invariant every other test sits on top of:
// parsing a notebook and asking for its bytes yields exactly what went in (§10).
func TestRoundTripAllFixtures(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			src := front + f.body
			n := mustParse(t, src)
			if got := string(n.Bytes()); got != src {
				t.Errorf("round trip changed bytes:\n got %q\nwant %q", got, src)
			}
			// Applying no edits must also be a no-op.
			out, err := n.Apply()
			if err != nil {
				t.Fatalf("Apply() = %v", err)
			}
			if string(out) != src {
				t.Errorf("Apply() with no edits changed bytes")
			}
		})
	}
}

func TestCellDetection(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		langs []string // one per detected cell, in document order
	}{
		{"simple", "## H\n\n```sh\na\n```\n", []string{"sh"}},
		{"no fence", "## H\n\nprose only\n", nil},
		{"no heading", "```sh\na\n```\n", nil},
		{"level 1 excluded", "# H\n\n```sh\na\n```\n", nil},
		{"level 6 included", "###### H\n\n```sh\na\n```\n", []string{"sh"}},
		{
			// §4.1: sections end at the next heading of any level, and cells
			// never nest, so both of these are cells in their own right.
			"nested headings both cells",
			"## Outer\n\n```sh\na\n```\n\n### Inner\n\n```cypher\nb\n```\n",
			[]string{"sh", "cypher"},
		},
		{
			// §4.3: first fence of any kind wins, so an untagged fence first
			// means no cell — no tool should guess which fence is the source.
			"untagged first fence",
			"## H\n\n```\nplain\n```\n\n```sh\na\n```\n",
			nil,
		},
		{
			"empty untagged first fence",
			"## H\n\n```\n```\n\n```sh\na\n```\n",
			nil,
		},
		{"output first fence", "## H\n\n```output\nr\n```\n\n```sh\na\n```\n", nil},
		{"error first fence", "## H\n\n```error\ne\n```\n\n```sh\na\n```\n", nil},
		{
			"language fence not first is inert",
			"## H\n\n```sh\nreal\n```\n\nprose\n\n```sh\nexample\n```\n",
			[]string{"sh"},
		},
		{
			"setext heading does not begin a section",
			"## Real\n\n```sh\na\n```\n\nUnderlined\n----------\n\n```sh\ninert\n```\n",
			[]string{"sh"},
		},
		{
			"hash inside a fence is not a heading",
			"## H\n\n```sh\n## inner\n```\n",
			[]string{"sh"},
		},
		{
			"blockquoted fence is not a cell",
			"## H\n\n> ```sh\n> a\n> ```\n",
			nil,
		},
		{"tilde fence", "## H\n\n~~~sh\na\n~~~\n", []string{"sh"}},
		{"indented fence", "## H\n\n   ```sh\na\n   ```\n", []string{"sh"}},
		{"unclosed fence", "## H\n\n```sh\nno close\n", []string{"sh"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := mustParse(t, front+tt.body)
			cells := n.Cells()
			if len(cells) != len(tt.langs) {
				var got []string
				for _, c := range cells {
					got = append(got, c.Lang)
				}
				t.Fatalf("got %d cells %v, want %d %v", len(cells), got, len(tt.langs), tt.langs)
			}
			for i, want := range tt.langs {
				if cells[i].Lang != want {
					t.Errorf("cell %d lang = %q, want %q", i, cells[i].Lang, want)
				}
			}
		})
	}
}

// TestSectionBoundaries pins §4.1: level is irrelevant and cells never nest, so an
// outer cell's section stops at the nested heading rather than swallowing it.
func TestSectionBoundaries(t *testing.T) {
	body := "## Outer\n\n```sh\na\n```\n\n### Inner\n\n```sh\nb\n```\n"
	n := mustParse(t, front+body)
	cells := n.Cells()
	if len(cells) != 2 {
		t.Fatalf("got %d cells, want 2", len(cells))
	}

	outer, inner := cells[0], cells[1]
	if got := string(outer.Section.In(n.Bytes())); got != "## Outer\n\n```sh\na\n```\n\n" {
		t.Errorf("outer section = %q", got)
	}
	if outer.Section.End != inner.Heading.Start {
		t.Errorf("outer section ends at %d, inner heading starts at %d — must abut",
			outer.Section.End, inner.Heading.Start)
	}
	if got := outer.SourceText(); got != "a\n" {
		t.Errorf("outer source = %q, want %q", got, "a\n")
	}
	if got := inner.SourceText(); got != "b\n" {
		t.Errorf("inner source = %q, want %q", got, "b\n")
	}
	// The inner fence must not be inside the outer cell's source or results.
	if outer.Source.End > inner.Heading.Start {
		t.Error("outer source fence overlaps the inner section")
	}
}

// TestHeadingBetweenFenceAndOutput pins that a heading breaks result pairing: the
// output fence belongs to a new section, where it is the first fence and therefore
// suppresses any cell.
func TestHeadingBetweenFenceAndOutput(t *testing.T) {
	body := "## H\n\n```sh\na\n```\n\n### Sub\n\n```output\nr\n```\n"
	n := mustParse(t, front+body)
	cells := n.Cells()
	if len(cells) != 1 {
		t.Fatalf("got %d cells, want 1", len(cells))
	}
	if len(cells[0].Results) != 0 {
		t.Errorf("got %d results, want 0 — a heading must break pairing", len(cells[0].Results))
	}
	if !cells[0].ResultPos.Empty() {
		t.Errorf("ResultPos = %v, want empty", cells[0].ResultPos)
	}
}

func TestResultPosition(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		forms []ResultForm
		// text is the exact bytes of the result position.
		text string
	}{
		{
			name:  "no results",
			body:  "## H\n\n```sh\na\n```\n",
			forms: nil,
			text:  "",
		},
		{
			name:  "one output",
			body:  "## H\n\n```sh\na\n```\n\n```output\nr\n```\n",
			forms: []ResultForm{ResultOutput},
			text:  "\n```output\nr\n```\n",
		},
		{
			name:  "one error",
			body:  "## H\n\n```sh\na\n```\n\n```error {status=1}\nboom\n```\n",
			forms: []ResultForm{ResultError},
			text:  "\n```error {status=1}\nboom\n```\n",
		},
		{
			name: "one sidecar",
			body: "## H\n\n```sh {id=aaaa2345}\na\n```\n\n" +
				"<!-- notekit:result kind=graph -->\n![x](n.assets/h--aaaa2345.png)\n",
			forms: []ResultForm{ResultSidecar},
			text:  "\n<!-- notekit:result kind=graph -->\n![x](n.assets/h--aaaa2345.png)\n",
		},
		{
			// §4.2: read tolerates several constructs; a run replaces them all.
			name:  "two outputs",
			body:  "## H\n\n```sh\na\n```\n\n```output\nfirst\n```\n\n```output\nsecond\n```\n",
			forms: []ResultForm{ResultOutput, ResultOutput},
			text:  "\n```output\nfirst\n```\n\n```output\nsecond\n```\n",
		},
		{
			name: "mixed forms",
			body: "## H\n\n```sh {id=bbbb2345}\na\n```\n\n```output\ntext\n```\n\n" +
				"<!-- notekit:result kind=graph -->\n![x](n.assets/h--bbbb2345.png)\n",
			forms: []ResultForm{ResultOutput, ResultSidecar},
			text:  "\n```output\ntext\n```\n\n<!-- notekit:result kind=graph -->\n![x](n.assets/h--bbbb2345.png)\n",
		},
		{
			name:  "no blank line before result",
			body:  "## H\n\n```sh\na\n```\n```output\nr\n```\n",
			forms: []ResultForm{ResultOutput},
			text:  "```output\nr\n```\n",
		},
		{
			name:  "prose terminates result position",
			body:  "## H\n\n```sh\na\n```\n\n```output\nr\n```\n\nProse after.\n",
			forms: []ResultForm{ResultOutput},
			text:  "\n```output\nr\n```\n",
		},
		{
			// §8: without a provenance comment an image link is always prose, so
			// a tool can never overwrite a user's illustration.
			name:  "bare image is prose",
			body:  "## H\n\n```sh\na\n```\n\n![plain](pic.png)\n",
			forms: nil,
			text:  "",
		},
		{
			name:  "ordinary comment is not provenance",
			body:  "## H\n\n```sh\na\n```\n\n<!-- a note -->\n![pic](p.png)\n",
			forms: nil,
			text:  "",
		},
		{
			name:  "provenance comment without image is prose",
			body:  "## H\n\n```sh\na\n```\n\n<!-- notekit:result kind=graph -->\nprose\n",
			forms: nil,
			text:  "",
		},
		{
			name:  "inert example fence does not become a result",
			body:  "## H\n\n```sh\na\n```\n\n```sh\nexample\n```\n",
			forms: nil,
			text:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := mustParse(t, front+tt.body)
			cells := n.Cells()
			if len(cells) != 1 {
				t.Fatalf("got %d cells, want 1", len(cells))
			}
			c := cells[0]

			if len(c.Results) != len(tt.forms) {
				t.Fatalf("got %d results, want %d", len(c.Results), len(tt.forms))
			}
			for i, want := range tt.forms {
				if c.Results[i].Form != want {
					t.Errorf("result %d form = %v, want %v", i, c.Results[i].Form, want)
				}
			}
			if got := string(c.ResultPos.In(n.Bytes())); got != tt.text {
				t.Errorf("result position = %q, want %q", got, tt.text)
			}
			// The result position must begin exactly where the source fence ends,
			// so replacing it can never disturb the fence.
			if c.ResultPos.Start != c.Source.End {
				t.Errorf("ResultPos.Start = %d, want Source.End = %d", c.ResultPos.Start, c.Source.End)
			}
		})
	}
}

func TestSidecarResultDetails(t *testing.T) {
	body := "## Module wiring\n\n```cypher {id=k3m7q2vf}\nMATCH (m) RETURN m\n```\n\n" +
		"<!-- notekit:result kind=graph, run=\"2026-07-16T10:02:55Z\", tool=\"graphtool/2.0\" -->\n" +
		"![Module wiring](n.assets/module-wiring--k3m7q2vf.png)\n"
	n := mustParse(t, front+body)
	c := n.Cells()[0]

	if len(c.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(c.Results))
	}
	r := c.Results[0]
	if r.Form != ResultSidecar {
		t.Fatalf("form = %v, want sidecar", r.Form)
	}
	if r.Dest != "n.assets/module-wiring--k3m7q2vf.png" {
		t.Errorf("Dest = %q", r.Dest)
	}
	// §8: the comment's attribute syntax is the §9 grammar without braces, so the
	// provenance metadata must be readable through the same parser.
	if r.MetaErr != nil {
		t.Fatalf("provenance metadata: %v", r.MetaErr)
	}
	if e, ok := r.Meta.Get("kind"); !ok || e.Value != "graph" {
		t.Errorf("kind = %#v, %v", e, ok)
	}
	if e, ok := r.Meta.Get("tool"); !ok || e.Value != "graphtool/2.0" {
		t.Errorf("tool = %#v, %v", e, ok)
	}
}

func TestMalformedInfoStringStillFormsACell(t *testing.T) {
	// §4.3 governs cell detection and §9 governs metadata; they are independent,
	// so a broken info string must not silently turn a cell into prose.
	body := "## H\n\n```sh {a=1, a=2}\ncode\n```\n"
	n := mustParse(t, front+body)
	cells := n.Cells()
	if len(cells) != 1 {
		t.Fatalf("got %d cells, want 1", len(cells))
	}
	if !cells[0].HasMetaError() {
		t.Error("HasMetaError() = false, want true for a duplicate key")
	}
	if cells[0].Lang != "sh" {
		t.Errorf("Lang = %q, want sh — the tag is still reportable", cells[0].Lang)
	}
}

func TestCellMetadataAndID(t *testing.T) {
	body := "## H\n\n```sh {format=csv, id=k3m7q2vf, custom=x}\ncode\n```\n"
	n := mustParse(t, front+body)
	c := n.Cells()[0]

	if c.ID != "k3m7q2vf" {
		t.Errorf("ID = %q, want k3m7q2vf", c.ID)
	}
	if c.MetaErr != nil {
		t.Fatalf("MetaErr = %v", c.MetaErr)
	}
	// All non-reserved keys are passthrough, delivered uninterpreted (§9).
	if e, ok := c.Meta.Get("custom"); !ok || e.Value != "x" {
		t.Errorf("custom = %#v, %v", e, ok)
	}
	if got := n.CellByID("k3m7q2vf"); got != c {
		t.Error("CellByID did not return the cell")
	}
	if got := n.CellByID("zzzz2345"); got != nil {
		t.Error("CellByID returned a cell for an unknown id")
	}
}

func TestDuplicateIDRejected(t *testing.T) {
	body := "## One\n\n```sh {id=k3m7q2vf}\na\n```\n\n## Two\n\n```sh {id=k3m7q2vf}\nb\n```\n"
	_, err := Parse([]byte(front + body))
	var dup *DuplicateIDError
	if !asError(err, &dup) {
		t.Fatalf("Parse = %v, want *DuplicateIDError", err)
	}
	if dup.ID != "k3m7q2vf" {
		t.Errorf("ID = %q", dup.ID)
	}
}
