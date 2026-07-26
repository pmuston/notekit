package doc

// Notebook is a parsed notekit notebook.
//
// A Notebook is immutable: every write produces new bytes through
// [Notebook.Apply], and the original source stays available for byte comparison.
// There is deliberately no method that serialises the document from its parsed
// structure — that absence is what guarantees round-trip identity (§10).
type Notebook struct {
	src   []byte
	fm    frontMatter
	body  Span
	cells []*Cell
}

// Parse parses a notekit notebook.
//
// It refuses any file that is not one — no front matter, or front matter without
// `notekit: 1` — rather than guessing at its structure (§2). Malformed constructs
// *within* a notebook are not errors: they are prose (§3). The one exception is a
// duplicate cell id, which §5.1 makes a tool error because it would otherwise
// attach two cells to the same sidecar files.
func Parse(src []byte) (*Notebook, error) {
	fm, bodyStart, err := parseFrontMatter(src)
	if err != nil {
		return nil, err
	}

	n := &Notebook{
		src:  src,
		fm:   fm,
		body: Span{Start: bodyStart, End: len(src)},
	}
	n.cells = buildCells(src, scanBlocks(src, bodyStart))

	seen := make(map[string]string, len(n.cells))
	for _, c := range n.cells {
		if c.ID == "" {
			continue
		}
		if prev, ok := seen[c.ID]; ok {
			return nil, &DuplicateIDError{ID: c.ID, Headings: [2]string{prev, c.HeadingText}}
		}
		seen[c.ID] = c.HeadingText
	}
	return n, nil
}

// Bytes returns the notebook's source, unchanged.
//
// Parse followed by Bytes is byte-identical by construction, since Bytes returns
// the very slice it was given. Round-trip identity is therefore a property of the
// write path, which [Notebook.Apply] enforces, not of a serialiser.
func (n *Notebook) Bytes() []byte { return n.src }

// Version returns the notebook's format version, always [Version] for a notebook
// Parse accepted.
func (n *Notebook) Version() int { return n.fm.version }

// Title returns the `title` front-matter scalar, empty when absent (§2).
func (n *Notebook) Title() string { return n.fm.title }

// FrontMatter returns the span of the front-matter block, including both `---`
// delimiter lines.
func (n *Notebook) FrontMatter() Span { return n.fm.span }

// Body returns the span of everything after the front matter.
func (n *Notebook) Body() Span { return n.body }

// Cells returns the notebook's cells in document order.
func (n *Notebook) Cells() []*Cell { return n.cells }

// Line returns the 1-based line number containing the given byte offset.
//
// Tools report problems as file:line so an editor can jump to them; spans are the
// parser's currency, so the conversion belongs here rather than in every tool.
func (n *Notebook) Line(offset int) int {
	if offset > len(n.src) {
		offset = len(n.src)
	}
	line := 1
	for i := 0; i < offset; i++ {
		if n.src[i] == '\n' {
			line++
		}
	}
	return line
}

// CellByID returns the cell carrying id, or nil.
func (n *Notebook) CellByID(id string) *Cell {
	for _, c := range n.cells {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// Preamble returns the span of content between the front matter and the first cell's
// section, which is where a notebook's opening prose lives.
//
// It covers the whole body when the notebook has no cells.
func (n *Notebook) Preamble() Span {
	if len(n.cells) == 0 {
		return n.body
	}
	return Span{Start: n.body.Start, End: n.cells[0].Section.Start}
}
