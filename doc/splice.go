package doc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pmuston/notekit/meta"
)

// Edit replaces one byte range with new text. An edit with an empty span is an
// insertion at that position.
type Edit struct {
	Span Span
	Text string
}

// Apply splices edits into the notebook and returns the new bytes.
//
// This is the only write path (§10). Edits may be supplied in any order but must
// not overlap; every byte outside them is copied through untouched, which is what
// makes round-trip identity hold for everything a tool did not write.
//
// Applying no edits returns the original bytes, so a no-op run cannot perturb a
// file.
func (n *Notebook) Apply(edits ...Edit) ([]byte, error) {
	if len(edits) == 0 {
		return n.src, nil
	}
	sorted := make([]Edit, len(edits))
	copy(sorted, edits)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Span.Start != sorted[j].Span.Start {
			return sorted[i].Span.Start < sorted[j].Span.Start
		}
		return sorted[i].Span.End < sorted[j].Span.End
	})

	for i, e := range sorted {
		if e.Span.Start < 0 || e.Span.End > len(n.src) || e.Span.Start > e.Span.End {
			return nil, fmt.Errorf("notekit: edit span %v is out of bounds for %d bytes", e.Span, len(n.src))
		}
		if i > 0 && e.Span.Start < sorted[i-1].Span.End {
			return nil, fmt.Errorf("notekit: overlapping edits at %v and %v", sorted[i-1].Span, e.Span)
		}
	}

	var b strings.Builder
	b.Grow(len(n.src))
	prev := 0
	for _, e := range sorted {
		b.Write(n.src[prev:e.Span.Start])
		b.WriteString(e.Text)
		prev = e.Span.End
	}
	b.Write(n.src[prev:])
	return []byte(b.String()), nil
}

// SetResult returns the edit that replaces the whole of the cell's result position
// with text (§4.2, §6: results are volatile).
//
// text is the result construct itself, without surrounding blank lines; the
// separating blank line is supplied here so that the same call works whether or not
// the cell already had results. Passing "" clears the result position.
//
// It fails when the source fence is unclosed. Such a fence extends to end of file,
// so there is no position after it: writing there would append into the fence body,
// and closing the fence first would be the silent repair §10 forbids. The cell can
// still be read and run — only persisting its result is impossible until the author
// closes the fence.
func (c *Cell) SetResult(text string) (Edit, error) {
	if !c.Closed {
		return Edit{}, fmt.Errorf(
			"notekit: cell %q has an unclosed source fence, so it has no result position; close the fence to persist results",
			c.HeadingText)
	}
	t := strings.Trim(text, "\n")
	if t == "" {
		return Edit{Span: c.ResultPos, Text: ""}, nil
	}
	// A leading newline creates the blank line after the source fence; when the
	// fence's own trailing newline is missing (end of file) one more is needed.
	lead := "\n"
	if c.ResultPos.Start > 0 && c.src[c.ResultPos.Start-1] != '\n' {
		lead = "\n\n"
	}
	return Edit{Span: c.ResultPos, Text: lead + t + "\n"}, nil
}

// AssignID returns the edit that writes id into the cell's source fence (§5.1).
//
// The insertion is append-only: every existing entry keeps its text, spacing, and
// order (§9). Assigning an id is the one edit a run makes to a *source* fence
// rather than to results, and it happens at most once in a cell's lifetime.
func (c *Cell) AssignID(id string) (Edit, error) {
	if c.ID != "" {
		return Edit{}, fmt.Errorf("notekit: cell %q already has id %q", c.HeadingText, c.ID)
	}
	if !ValidID(id) {
		return Edit{}, fmt.Errorf("notekit: %q is not a valid cell id (%d lower-case base32 characters)", id, IDLen)
	}
	if c.MetaErr != nil {
		return Edit{}, fmt.Errorf("notekit: cell %q has a malformed info string: %w", c.HeadingText, c.MetaErr)
	}
	info, err := c.Meta.Insert(KeyID, id)
	if err != nil {
		return Edit{}, fmt.Errorf("notekit: cell %q: %w", c.HeadingText, err)
	}
	return Edit{Span: c.InfoSpan, Text: info}, nil
}

// EditProse returns the edit that replaces a prose range.
//
// The caller is responsible for the span lying outside every cell's source fence
// and result position; [Notebook.Apply] rejects overlaps but cannot tell prose from
// anything else.
func (n *Notebook) EditProse(span Span, text string) Edit {
	return Edit{Span: span, Text: text}
}

// SetSource returns the edit that replaces the cell's fence body with text,
// rewriting the whole source fence.
//
// The fence is re-emitted rather than only its body, because fence-length safety
// applies to a source fence as much as to a result (§6): a body containing a run of
// backticks at least as long as the fence would terminate it early and silently turn the
// rest of the cell into prose. Widening the fence is not silent repair — §10 permits a
// tool to rewrite the byte ranges it edited, and the source fence is precisely what is
// being edited here. The info string is preserved exactly, including any `id`.
//
// It fails on an unclosed source fence, for the same reason [Cell.SetResult] does: such
// a fence runs to end of file, and closing it on the author's behalf would be the repair
// §10 forbids. `notefmt` reports the condition so the author can fix it.
func (c *Cell) SetSource(text string) (Edit, error) {
	if !c.Closed {
		return Edit{}, fmt.Errorf(
			"notekit: cell %q has an unclosed source fence; close it before editing the cell",
			c.HeadingText)
	}
	ch := c.fenceCh
	if ch != '`' && ch != '~' {
		ch = '`'
	}

	body := text
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	// A tilde fence's length is bounded by tilde runs, a backtick fence's by backtick
	// runs; measuring the wrong character would leave the fence breakable.
	n := runLen(body, ch) + 1
	if n < 3 {
		n = 3
	}
	fence := strings.Repeat(string(ch), n)

	return Edit{
		Span: c.Source,
		Text: fence + c.Info + "\n" + body + fence + "\n",
	}, nil
}

// runLen returns the longest run of ch in s.
func runLen(s string, ch byte) int {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == ch {
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}
	return longest
}

// NewCell describes a cell to insert (§10 f).
//
// A new cell is a heading plus a source fence and nothing else. Deliberately so: a tool
// that also invented prose, metadata, or a placeholder result would be writing content
// the user did not ask for, into a file whose whole point is that the user owns it.
type NewCell struct {
	// Heading is the heading text, without the leading #'s. It may be empty — `##`
	// alone is a valid ATX heading, and an empty slug is permitted (§5.2) — but it
	// must not contain a newline, which would end the heading line early.
	Heading string

	// Level is the ATX heading level, 2–6 (§4). Zero means 2, which §4 recommends.
	Level int

	// Lang is the info-string tag the executor will match on.
	Lang string

	// Meta is optional source-fence metadata, e.g. a `format` rendering hint. The
	// format assigns no meaning to any of it (§9).
	Meta []meta.Entry

	// Body is the cell's source. It may be empty.
	Body string
}

// AppendCell returns the edit that adds a cell at the end of the notebook.
func (n *Notebook) AppendCell(spec NewCell) (Edit, error) {
	return n.insertCellAt(spec, len(n.src))
}

// InsertCellAfter returns the edit that adds a cell immediately after c's section.
//
// A section runs to the next heading of any level (§4.1), so its end is exactly a section
// boundary — which is the only place a new heading can go without splitting an existing
// cell in two.
func (n *Notebook) InsertCellAfter(c *Cell, spec NewCell) (Edit, error) {
	if c == nil {
		return Edit{}, fmt.Errorf("notekit: InsertCellAfter needs a cell")
	}
	return n.insertCellAt(spec, c.Section.End)
}

// InsertCellBefore returns the edit that adds a cell immediately before c's section.
func (n *Notebook) InsertCellBefore(c *Cell, spec NewCell) (Edit, error) {
	if c == nil {
		return Edit{}, fmt.Errorf("notekit: InsertCellBefore needs a cell")
	}
	return n.insertCellAt(spec, c.Section.Start)
}

// DeleteCell returns the edit that removes a cell and everything else in its section:
// its heading, its prose, its source fence, and its results.
//
// That is what "delete this cell" means — a section is the unit §4.1 defines, and leaving
// a heading behind with no fence, or prose belonging to a cell that no longer exists,
// would be a stranger outcome than removing the lot. To clear only a result, use
// [Cell.SetResult] with an empty string.
func (n *Notebook) DeleteCell(c *Cell) (Edit, error) {
	if c == nil {
		return Edit{}, fmt.Errorf("notekit: DeleteCell needs a cell")
	}
	return Edit{Span: c.Section, Text: ""}, nil
}

// insertCellAt builds the insertion, including the whitespace needed to seat a heading at
// a byte offset that may or may not be at a line start.
func (n *Notebook) insertCellAt(spec NewCell, at int) (Edit, error) {
	if at < n.body.Start {
		at = n.body.Start
	}
	if at > len(n.src) {
		at = len(n.src)
	}

	block, err := spec.render()
	if err != nil {
		return Edit{}, err
	}

	// A heading must begin a line, and reads far better with a blank line above it.
	var pre string
	switch {
	case at == 0 || at == n.body.Start:
		pre = ""
	case n.src[at-1] != '\n':
		// Not at a line start: end the current line, then leave a blank one.
		pre = "\n\n"
	case at == 1 || n.src[at-2] == '\n':
		pre = "" // a blank line is already there
	default:
		pre = "\n"
	}

	// Separate from whatever follows, unless it already begins with a blank line.
	var post string
	if at < len(n.src) && n.src[at] != '\n' {
		post = "\n"
	}

	return Edit{Span: Span{Start: at, End: at}, Text: pre + block + post}, nil
}

// render turns a spec into the bytes of a cell.
func (spec NewCell) render() (string, error) {
	level := spec.Level
	if level == 0 {
		level = 2
	}
	if level < 2 || level > 6 {
		return "", fmt.Errorf("notekit: heading level %d is out of range (2–6, §4)", level)
	}
	if strings.ContainsAny(spec.Heading, "\r\n") {
		return "", fmt.Errorf("notekit: a heading cannot contain a newline")
	}
	// A fence tagged output or error is not a source fence — as a section's first fence
	// it suppresses the cell entirely (§4.3) — so creating one would produce a cell that
	// is not a cell.
	switch spec.Lang {
	case "":
		return "", fmt.Errorf("notekit: a cell needs a language tag (§4)")
	case "output", "error":
		return "", fmt.Errorf("notekit: %q is a result tag, not a language tag; "+
			"a section whose first fence is tagged %q contains no cell (§4.3)",
			spec.Lang, spec.Lang)
	}

	info, err := meta.Format(spec.Lang, spec.Meta, nil)
	if err != nil {
		return "", fmt.Errorf("notekit: building the source fence: %w", err)
	}

	body := spec.Body
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	// Fence-length safety applies to a source fence as much as to a result (§6).
	fenceLen := runLen(body, '`') + 1
	if fenceLen < 3 {
		fenceLen = 3
	}
	fence := strings.Repeat("`", fenceLen)

	heading := strings.Repeat("#", level)
	if spec.Heading != "" {
		heading += " " + spec.Heading
	}

	return heading + "\n\n" + fence + info + "\n" + body + fence + "\n", nil
}
